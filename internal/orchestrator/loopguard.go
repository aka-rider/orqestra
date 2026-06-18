package orchestrator

import (
	"context"
	"fmt"
	"sync"

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

// steeringSink wraps a downstream sink, intercepts EventToolUse/EventToolResult
// events for loop detection, and writes nudge decisions to a buffered channel.
type steeringSink struct {
	inner     harness.Sink
	detector  *loopDetector
	nudgeCh   chan<- loopAction
	mu        sync.Mutex
	lastError bool // tracks whether last EventToolResult was an error
}

func (s *steeringSink) Observe(ev harness.Event) {
	if s.inner != nil {
		s.inner.Observe(ev)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch ev.Kind {
	case harness.EventToolResult:
		s.lastError = ev.IsError
	case harness.EventToolUse:
		action := s.detector.observe(ev.Tool, ev.Args, s.lastError)
		s.lastError = false
		if action != loopNone {
			select {
			case s.nudgeCh <- action:
			default:
			}
		}
	}
}

// steeringExecutor implements loop-detection steering. When SteerOnLoop is
// false it is a transparent passthrough. When true it:
//  1. Opens an input plane (myIn channel) and posts spec.Prompt as the first message.
//  2. Wraps the sink with a loop detector.
//  3. Listens for nudge/escalate signals: nudge → posts a correction message;
//     escalate → cancels the child context and returns ErrLoopEscalated.
//
// Ownership: myIn is owned by steeringExecutor. The real harness.Run exits its
// input-plane goroutine via runDone (process exit), independently of myIn being
// closed — so we only close myIn on escalation, not on normal exit.
type steeringExecutor struct {
	inner harness.Executor
}

// ErrLoopEscalated is returned when the steering executor forcibly stops a run
// because the loop guard exhausted all nudges.
var ErrLoopEscalated = fmt.Errorf("loop guard: escalated after repeated identical tool calls")

// NewSteeringExecutor returns an Executor that applies loop-guard steering when
// spec.SteerOnLoop is true. Otherwise it is a transparent passthrough.
func NewSteeringExecutor(inner harness.Executor) harness.Executor {
	return &steeringExecutor{inner: inner}
}

func (s *steeringExecutor) Run(ctx context.Context, spec harness.ProcessSpec, in <-chan harness.Message, sink harness.Sink) (harness.RunResult, error) {
	if !spec.SteerOnLoop {
		return s.inner.Run(ctx, spec, in, sink)
	}

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	myIn := make(chan harness.Message, 16)
	if spec.Prompt != "" {
		myIn <- harness.Message{Text: spec.Prompt}
		spec.Prompt = ""
	}

	detector := newLoopDetector(spec.LoopGuard)
	nudgeCh := make(chan loopAction, 4)
	wrapped := &steeringSink{inner: sink, detector: detector, nudgeCh: nudgeCh}

	// myInClosed guards a single close(myIn) on escalation.
	var myInOnce sync.Once
	closeMyIn := func() { myInOnce.Do(func() { close(myIn) }) }

	controlDone := make(chan struct{})
	var escalated bool
	go func() {
		defer close(controlDone)
		for {
			select {
			case action, ok := <-nudgeCh:
				if !ok {
					return
				}
				switch action {
				case loopNudge:
					msg := harness.Message{Text: "You appear to be calling the same tool repeatedly " +
						"with identical arguments. Step back: re-read what you already know, pick a " +
						"different approach, or call SubmitReport to deliver your findings so far."}
					select {
					case myIn <- msg:
					case <-childCtx.Done():
						return
					}
				case loopEscalate:
					escalated = true
					cancel()
					closeMyIn()
					return
				}
			case <-childCtx.Done():
				return
			}
		}
	}()

	res, err := s.inner.Run(childCtx, spec, myIn, wrapped)

	// Signal controller to drain and exit.
	close(nudgeCh)
	<-controlDone

	if escalated {
		return res, ErrLoopEscalated
	}
	return res, err
}
