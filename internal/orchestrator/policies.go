package orchestrator

import (
	"fmt"
	"regexp"
	"time"

	"github.com/xiii/orqestra/internal/harness"
)

// policyResult is the action a policy returns from observe or tick.
// Zero value = no action. Non-empty text = send as nudge. stop = escalate,
// and err is the cause the supervisor should cancel with — every policy that
// sets stop must set a descriptive err so escalations aren't mislabeled.
type policyResult struct {
	text string
	stop bool
	err  error
}

// supervisorPolicy is observed from AgentSupervisor's event loop.
// observe is called synchronously for each harness.Event.
// tick is called on each ticker fire; deadline is zero when ctx has no deadline.
// Implementations are NOT concurrent-safe — they run only on the supervisor goroutine.
type supervisorPolicy interface {
	observe(ev harness.Event) policyResult
	tick(now, lastEvent, deadline time.Time) policyResult
}

// basePolicy provides no-op defaults so concrete policies only override what they need.
type basePolicy struct{}

func (basePolicy) observe(_ harness.Event) policyResult { return policyResult{} }
func (basePolicy) tick(_, _, _ time.Time) policyResult  { return policyResult{} }

// -- loopPolicy ---------------------------------------------------------------

const loopNudgeText = "You appear to be calling the same tool repeatedly " +
	"with identical arguments. Step back: re-read what you already know, pick a " +
	"different approach, or write your plan now to record your findings so far."

type loopPolicy struct {
	basePolicy
	d         *loopDetector
	lastError bool
}

func newLoopPolicy(spec harness.LoopGuardSpec) *loopPolicy {
	return &loopPolicy{d: newLoopDetector(spec)}
}

func (p *loopPolicy) observe(ev harness.Event) policyResult {
	switch ev.Kind {
	case harness.EventToolResult:
		p.lastError = ev.IsError
	case harness.EventToolUse:
		action := p.d.observe(ev.Tool, ev.Args, p.lastError)
		p.lastError = false
		switch action {
		case loopNudge:
			return policyResult{text: loopNudgeText}
		case loopEscalate:
			return policyResult{stop: true, err: fmt.Errorf(
				"loop guard: escalated after repeated calls to %s (%d nudges sent): %w",
				ev.Tool, p.d.nudgeCount, ErrLoopEscalated)}
		}
	}
	return policyResult{}
}

// -- silencePolicy ------------------------------------------------------------
//
// silencePolicy escalates only after structural evidence of a stall — a
// completed assistant turn (EventChunk{IsDelta:false}) with no tool call
// since the previous turn boundary — never from elapsed silence alone. A
// non-streaming provider emits zero events while generating a single
// response, so "no events for N seconds" is indistinguishable from "still
// generating" until at least one turn has actually completed. Once a
// confirmed-empty turn has been observed, further silence is unambiguous:
// nothing is in flight, the subprocess is idle. A hang with no completed
// turn at all is left to the per-agent hard timeout, which this guard does
// not attempt to shortcut.
//
// toolUseSinceBoundary is evaluated LIVE in tick(), not frozen at the moment
// the boundary event arrives: a single assistant message can carry both text
// and a tool call (streamEventsFrom emits the text EventChunk before that
// same message's EventToolUse), so the tool call for the CURRENT boundary is
// only known a moment after the boundary itself is observed. Deciding
// "empty" at boundary time would misclassify every such combined turn as
// empty until the next boundary arrived — falsely arming escalation during
// perfectly healthy tool-calling work. Checking it live at tick() sidesteps
// that ordering entirely: by the time a tick can fire, any trailing tool
// call for the same message has already been processed.
type silencePolicy struct {
	basePolicy
	silenceDur           time.Duration
	nudgeText            string
	maxNudges            int
	nudgeCount           int
	lastNudge            time.Time // zero = never nudged
	sawBoundary          bool      // at least one completed assistant turn has been observed
	toolUseSinceBoundary bool      // live: a tool call has happened since the most recent boundary
}

func (p *silencePolicy) observe(ev harness.Event) policyResult {
	switch ev.Kind {
	case harness.EventToolUse:
		if p.sawBoundary && !p.toolUseSinceBoundary {
			// Recovery: a tool call after an empty stretch. Reset the budget —
			// this is deliberately NOT reset on every boundary (see tick): a
			// model that keeps producing empty turns after being nudged must
			// still exhaust its budget and escalate, not loop forever.
			p.nudgeCount = 0
			p.lastNudge = time.Time{}
		}
		p.toolUseSinceBoundary = true
	case harness.EventChunk:
		if !ev.IsDelta { // turn boundary: a whole assistant message just completed
			p.sawBoundary = true
			p.toolUseSinceBoundary = false
		}
	}
	return policyResult{}
}

func (p *silencePolicy) tick(now, lastEvent, _ time.Time) policyResult {
	if !p.sawBoundary || p.toolUseSinceBoundary {
		return policyResult{} // no confirmed-empty turn right now — ambiguous or productive; defer to the hard timeout
	}
	if now.Sub(lastEvent) < p.silenceDur {
		return policyResult{}
	}
	if !p.lastNudge.IsZero() && now.Sub(p.lastNudge) < p.silenceDur {
		return policyResult{}
	}
	if p.nudgeCount >= p.maxNudges {
		return policyResult{stop: true, err: fmt.Errorf(
			"silence guard: escalated after %d nudges with no productive turn following an empty response: %w",
			p.maxNudges, ErrSilenceEscalated)}
	}
	p.lastNudge = now
	p.nudgeCount++
	return policyResult{text: p.nudgeText}
}

// -- preTimeoutPolicy ---------------------------------------------------------

type preTimeoutPolicy struct {
	basePolicy
	nudgeText string
	fired     bool
}

func (p *preTimeoutPolicy) tick(now, _ time.Time, deadline time.Time) policyResult {
	if p.fired || deadline.IsZero() {
		return policyResult{}
	}
	if deadline.Sub(now) <= preTimeoutWarning {
		p.fired = true
		return policyResult{text: p.nudgeText}
	}
	return policyResult{}
}

// -- driftPolicy --------------------------------------------------------------

// reImplementationIntent matches when an agent expresses intent to implement
// rather than submit its report (the architect/researcher "drift" tell).
var reImplementationIntent = regexp.MustCompile(
	`(?i)\b(?:i'?ll|i will|let me|let's|now i'?ll|ready to|start)\s+(?:start\s+|now\s+)?implement`)

type driftPolicy struct {
	basePolicy
	nudgeText  string
	standDown  bool
	nudgedOnce bool
}

func (p *driftPolicy) observe(ev harness.Event) policyResult {
	if p.standDown {
		return policyResult{}
	}
	switch ev.Kind {
	case harness.EventChunk:
		// Only whole assistant messages (not streaming deltas) carry the full text needed
		// for reliable regex matching. IsDelta fragments are partial — skip them.
		if ev.IsDelta {
			return policyResult{}
		}
		if !p.nudgedOnce && reImplementationIntent.MatchString(ev.Text) {
			p.nudgedOnce = true
			return policyResult{text: p.nudgeText}
		}
	case harness.EventToolUse:
		if ev.Tool == "mcp__orqestra__SubmitReport" {
			p.standDown = true
		}
	}
	return policyResult{}
}
