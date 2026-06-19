package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
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

// steeringSink wraps a downstream sink, intercepts EventToolUse/EventToolResult
// events for loop detection, and writes nudge decisions to a buffered channel.
type steeringSink struct {
	inner     harness.Sink
	detector  *loopDetector
	nudgeCh   chan<- loopAction
	mu        sync.Mutex
	lastError bool         // tracks whether last EventToolResult was an error
	lastEvent atomic.Int64 // Unix ns of last Observe call; 0 = no events yet
}

func (s *steeringSink) Observe(ev harness.Event) {
	s.lastEvent.Store(time.Now().UnixNano())
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

// preTimeoutWarning is how far before the hard deadline the pre-timeout nudge fires.
const preTimeoutWarning = 60 * time.Second

// steeringExecutor implements loop-detection steering. When both SteerOnLoop is
// false and PreTimeoutNudge is empty it is a transparent passthrough. Otherwise:
//  1. Opens an input plane (myIn channel) and posts spec.Prompt as the first message.
//  2. When SteerOnLoop=true: wraps the sink with a loop detector and silence detector.
//  3. When PreTimeoutNudge is set: fires a role-specific message 60 s before the deadline.
//  4. Listens for nudge/escalate signals: nudge → posts a correction message;
//     escalate → cancels the child context and returns ErrLoopEscalated.
//
// Goroutine lifecycle (steerable path):
//   - control goroutine: reads nudgeCh; exits when nudgeCh is closed (ok=false) or childCtx done.
//   - silence goroutine: sends to nudgeCh; exits via silenceCtx.Done() (derived from childCtx).
//   - pre-timeout goroutine: sends to myIn; exits via childCtx.Done().
//
// Shutdown order:
//  1. inner.Run returns.
//  2. silenceCancel() — stops the silence goroutine via silenceCtx (NOT childCtx, so the
//     control goroutine remains live and can finish delivering queued nudges to myIn).
//  3. sendersDone.Wait() — ensures no goroutine sends to nudgeCh anymore.
//  4. close(nudgeCh) — lets the control goroutine drain all remaining items and exit.
//  5. <-controlDone — waits for the control goroutine to finish.
//  6. cancel() — cancels childCtx, stopping the pre-timeout goroutine.
//  7. cleanupDone.Wait() — waits for the pre-timeout goroutine.
//
// This ordering guarantees:
//   - No send-on-closed-channel panic (sendersDone before close).
//   - Control goroutine delivers all queued nudges (including loopEscalate) before Run returns.
type steeringExecutor struct {
	inner harness.Executor
}

// ErrLoopEscalated is returned when the steering executor forcibly stops a run
// because the loop guard exhausted all nudges.
var ErrLoopEscalated = fmt.Errorf("loop guard: escalated after repeated identical tool calls")

// NewSteeringExecutor returns an Executor that applies loop-guard steering when
// spec.SteerOnLoop is true, or sends a pre-timeout nudge when spec.PreTimeoutNudge
// is set. Otherwise it is a transparent passthrough.
func NewSteeringExecutor(inner harness.Executor) harness.Executor {
	return &steeringExecutor{inner: inner}
}

func (s *steeringExecutor) Run(ctx context.Context, spec harness.ProcessSpec, in <-chan harness.Message, sink harness.Sink) (harness.RunResult, error) {
	if !spec.SteerOnLoop && spec.PreTimeoutNudge == "" {
		return s.inner.Run(ctx, spec, in, sink)
	}

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	myIn := make(chan harness.Message, 16)
	if spec.Prompt != "" {
		myIn <- harness.Message{Text: spec.Prompt}
		spec.Prompt = ""
	}

	var myInOnce sync.Once
	closeMyIn := func() { myInOnce.Do(func() { close(myIn) }) }
	var escalated bool

	// sendersDone tracks goroutines that write to nudgeCh; they must all exit
	// before close(nudgeCh) to prevent send-on-closed-channel panics.
	var sendersDone sync.WaitGroup

	// cleanupDone tracks goroutines that write to myIn but not nudgeCh
	// (pre-timeout goroutine). Waited last, after controlDone.
	var cleanupDone sync.WaitGroup

	// Set up loop detection and control goroutine only when SteerOnLoop is enabled.
	var steerSink *steeringSink
	var nudgeCh chan loopAction
	controlDone := make(chan struct{})

	if spec.SteerOnLoop {
		nudgeCh = make(chan loopAction, 4)
		detector := newLoopDetector(spec.LoopGuard)
		steerSink = &steeringSink{inner: sink, detector: detector, nudgeCh: nudgeCh}

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
						nudgeText := spec.PreTimeoutNudge
						if nudgeText == "" {
							nudgeText = "You appear to be calling the same tool repeatedly " +
								"with identical arguments. Step back: re-read what you already know, pick a " +
								"different approach, or call SubmitReport to deliver your findings so far."
						}
						select {
						case myIn <- harness.Message{Text: nudgeText}:
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
	} else {
		close(controlDone) // no control goroutine; make drain a no-op
	}

	// Determine which sink the inner executor sees.
	var activeSink harness.Sink
	if steerSink != nil {
		activeSink = steerSink
	} else {
		activeSink = sink
	}

	// Pre-timeout goroutine: fires once, 60 s before the hard deadline.
	// Writes to myIn (not nudgeCh), so it belongs in cleanupDone.
	if spec.PreTimeoutNudge != "" {
		if deadline, ok := ctx.Deadline(); ok {
			warnIn := time.Until(deadline) - preTimeoutWarning
			if warnIn < 0 {
				warnIn = 0
			}
			cleanupDone.Add(1)
			go func() {
				defer cleanupDone.Done()
				select {
				case <-time.After(warnIn):
					select {
					case myIn <- harness.Message{Text: spec.PreTimeoutNudge}:
					default: // buffer full — drop
					}
				case <-childCtx.Done():
				}
			}()
		}
	}

	// Silence detector goroutine: fires when no harness events arrive for SilenceSecs.
	// Only active when SteerOnLoop=true (steerSink tracks lastEvent).
	// Writes to nudgeCh, so it belongs in sendersDone.
	// Uses ns > 0 to detect "no events yet" since lastEvent starts at 0, not time.IsZero.
	//
	// silenceCancel is a separate cancel derived from childCtx: we stop only the senders
	// before close(nudgeCh), without canceling childCtx (which would interrupt the control
	// goroutine mid-nudge delivery and lose queued escalations).
	silenceCtx, silenceCancel := context.WithCancel(childCtx)
	defer silenceCancel()
	if steerSink != nil && spec.LoopGuard.SilenceSecs > 0 {
		silenceDur := time.Duration(spec.LoopGuard.SilenceSecs) * time.Second
		sendersDone.Add(1)
		go func() {
			defer sendersDone.Done()
			ticker := time.NewTicker(silenceDur / 2)
			defer ticker.Stop()
			for {
				select {
				case <-silenceCtx.Done():
					return
				case <-ticker.C:
					if ns := steerSink.lastEvent.Load(); ns > 0 && time.Since(time.Unix(0, ns)) >= silenceDur {
						select {
						case nudgeCh <- loopNudge:
						default:
						}
					}
				}
			}
		}()
	}

	res, err := s.inner.Run(childCtx, spec, myIn, activeSink)

	// Shutdown sequence (see comment on steeringExecutor for the rationale):
	// 1. silenceCancel() stops the silence goroutine without canceling childCtx,
	//    so the control goroutine can still deliver its queued nudges to myIn.
	// 2. sendersDone.Wait() ensures nobody sends to nudgeCh after close.
	// 3. close(nudgeCh) drains the control goroutine: it reads remaining items
	//    (including loopEscalate), then exits when ok=false.
	// 4. <-controlDone waits for the control goroutine to finish.
	// 5. cancel() cancels childCtx (cleans up pre-timeout goroutine).
	// 6. cleanupDone.Wait() waits for the pre-timeout goroutine.
	silenceCancel()
	sendersDone.Wait()
	if nudgeCh != nil {
		close(nudgeCh)
	}
	<-controlDone
	cancel()
	cleanupDone.Wait()

	if escalated {
		return res, ErrLoopEscalated
	}
	return res, err
}
