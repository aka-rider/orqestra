package orchestrator

import (
	"fmt"
	"time"

	"github.com/xiii/orqestra/internal/harness"
)

// loopDetector watches a stream of EventToolUse events and detects repeated
// identical calls. State machine: idle → counting → nudging → escalated.
// fingerprint = tool + "\x00" + args (compact JSON).
type loopDetector struct {
	spec        harness.LoopGuardSpec
	fingerprint string
	count       int  // consecutive repeats of current fingerprint
	nudgeCount  int  // nudges sent so far
	cooldown    int  // turns remaining in cooldown
	escalated   bool
}

func newLoopDetector(spec harness.LoopGuardSpec) *loopDetector {
	return &loopDetector{spec: spec}
}

type loopAction int

const (
	loopNone     loopAction = iota
	loopNudge               // send a steering message
	loopEscalate            // cancel the run
)

// observe records one EventToolUse and returns the action to take.
// errResult is true when the preceding tool_result was an error — makes the
// effective contribution per call higher (weight=2) to trip the threshold faster.
func (d *loopDetector) observe(tool, args string, errResult bool) loopAction {
	if d.escalated {
		return loopEscalate
	}
	if d.cooldown > 0 {
		d.cooldown--
		return loopNone
	}

	fp := tool + "\x00" + args
	weight := 1
	if errResult {
		weight = 2
	}

	if fp != d.fingerprint {
		d.fingerprint = fp
		d.count = weight
		return loopNone
	}
	d.count += weight

	threshold := d.spec.RepeatThreshold
	if threshold <= 0 {
		threshold = 3
	}
	if d.count < threshold {
		return loopNone
	}

	maxNudges := d.spec.MaxNudges
	if maxNudges <= 0 {
		maxNudges = 3
	}
	if d.nudgeCount >= maxNudges {
		d.escalated = true
		return loopEscalate
	}

	d.nudgeCount++
	cooldown := d.spec.CooldownTurns
	if cooldown <= 0 {
		cooldown = 2
	}
	d.cooldown = cooldown
	d.count = 0
	return loopNudge
}

// ErrLoopEscalated is returned when the supervisor forcibly stops a run
// because the loop guard exhausted all nudges.
var ErrLoopEscalated = fmt.Errorf("loop guard: escalated after repeated identical tool calls")

// preTimeoutWarning is how far before the hard deadline the pre-timeout nudge fires.
const preTimeoutWarning = 60 * time.Second
