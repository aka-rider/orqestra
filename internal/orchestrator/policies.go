package orchestrator

import (
	"regexp"
	"time"

	"github.com/xiii/orqestra/internal/harness"
)

// policyResult is the action a policy returns from observe or tick.
// Zero value = no action. Non-empty text = send as nudge. stop = escalate.
type policyResult struct {
	text string
	stop bool
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

func (basePolicy) observe(_ harness.Event) policyResult                         { return policyResult{} }
func (basePolicy) tick(_, _, _ time.Time) policyResult                          { return policyResult{} }

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
			return policyResult{stop: true}
		}
	}
	return policyResult{}
}

// -- silencePolicy ------------------------------------------------------------

type silencePolicy struct {
	basePolicy
	silenceDur time.Duration
	nudgeText  string
}

func (p *silencePolicy) tick(now, lastEvent, _ time.Time) policyResult {
	if now.Sub(lastEvent) >= p.silenceDur {
		return policyResult{text: p.nudgeText}
	}
	return policyResult{}
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
