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

// ErrLoopEscalated is returned when the loop breaker forcibly stops a run
// because the loop guard exhausted all nudges.
var ErrLoopEscalated = fmt.Errorf("loop guard: escalated after repeated identical tool calls")

// preTimeoutWarning is how far before the hard deadline the pre-timeout nudge fires.
const preTimeoutWarning = 60 * time.Second

// Middleware wraps one Executor with one cross-cutting behaviour.
// Each New* function returns a zero-field Middleware value.
type Middleware interface {
	Wrap(harness.Executor) harness.Executor
}

// ExecutorBuilder assembles a chain of Middleware around a base Executor.
// With adds middleware in outermost-first order; Wrap applies them so the
// last With call becomes the innermost wrapper around base.
type ExecutorBuilder struct{ stack []Middleware }

// NewExecutorBuilder returns an empty builder.
func NewExecutorBuilder() *ExecutorBuilder { return &ExecutorBuilder{} }

// With appends m to the builder (outermost-first order) and returns the builder.
func (b *ExecutorBuilder) With(m Middleware) *ExecutorBuilder {
	b.stack = append(b.stack, m)
	return b
}

// Wrap applies the accumulated middleware stack around base, returning the
// composed Executor. The last With call produces the innermost wrapper.
func (b *ExecutorBuilder) Wrap(base harness.Executor) harness.Executor {
	for i := len(b.stack) - 1; i >= 0; i-- {
		base = b.stack[i].Wrap(base)
	}
	return base
}

// mergeMessages fans in two channels; nil inputs are skipped.
// Returns the bidirectional merged channel and a wait function that blocks until
// all pump goroutines exit. Call wait() after cancel() in each middleware's
// shutdown to prevent goroutine leaks. Callers may also pre-fill out before
// passing it to an inner executor (the buffer is 8).
func mergeMessages(ctx context.Context, a, b <-chan harness.Message) (chan harness.Message, func()) {
	out := make(chan harness.Message, 8)
	var wg sync.WaitGroup
	pump := func(ch <-chan harness.Message) {
		defer wg.Done()
		if ch == nil {
			return
		}
		for {
			select {
			case msg, ok := <-ch:
				if !ok {
					return
				}
				select {
				case out <- msg:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}
	wg.Add(2)
	go pump(a)
	go pump(b)
	go func() { wg.Wait(); close(out) }()
	return out, wg.Wait
}

// -- PreTimeoutNudger -------------------------------------------------------

type preTimeoutNudger struct{}

// NewPreTimeoutNudger returns a Middleware that fires spec.PreTimeoutNudge into
// the input plane 60 s before the context deadline. Passthrough when
// PreTimeoutNudge is empty.
func NewPreTimeoutNudger() Middleware { return preTimeoutNudger{} }

func (preTimeoutNudger) Wrap(inner harness.Executor) harness.Executor {
	return &preTimeoutNudgerExec{inner: inner}
}

type preTimeoutNudgerExec struct{ inner harness.Executor }

func (e *preTimeoutNudgerExec) Run(ctx context.Context, spec harness.ProcessSpec, in <-chan harness.Message, sink harness.Sink) (harness.RunResult, error) {
	if spec.PreTimeoutNudge == "" {
		return e.inner.Run(ctx, spec, in, sink)
	}

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	myIn := make(chan harness.Message, 16)
	if spec.Prompt != "" {
		myIn <- harness.Message{Text: spec.Prompt}
		spec.Prompt = ""
	}

	var cleanupDone sync.WaitGroup
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

	res, err := e.inner.Run(childCtx, spec, myIn, sink)
	cancel()
	cleanupDone.Wait()
	return res, err
}

// -- LoopBreaker ------------------------------------------------------------

type loopBreaker struct{}

// NewLoopBreaker returns a Middleware that detects repeated identical tool calls
// and sends a nudge message to the input plane; cancels the run after MaxNudges.
// Passthrough when spec.LoopGuard is zero.
func NewLoopBreaker() Middleware { return loopBreaker{} }

func (loopBreaker) Wrap(inner harness.Executor) harness.Executor {
	return &loopBreakerExec{inner: inner}
}

type loopBreakerExec struct{ inner harness.Executor }

// loopDetectorSink intercepts EventToolUse/EventToolResult for loop detection.
type loopDetectorSink struct {
	inner     harness.Sink
	detector  *loopDetector
	nudgeCh   chan<- loopAction
	mu        sync.Mutex
	lastError bool
}

func (s *loopDetectorSink) Observe(ev harness.Event) {
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

func (e *loopBreakerExec) Run(ctx context.Context, spec harness.ProcessSpec, in <-chan harness.Message, sink harness.Sink) (harness.RunResult, error) {
	if (spec.LoopGuard == harness.LoopGuardSpec{}) {
		return e.inner.Run(ctx, spec, in, sink)
	}

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// internalNudgeCh carries loopAction signals from the sink's Observe.
	// msgCh carries harness.Message nudges for the input plane.
	internalNudgeCh := make(chan loopAction, 4)
	msgCh := make(chan harness.Message, 4)

	detector := newLoopDetector(spec.LoopGuard)
	lSink := &loopDetectorSink{inner: sink, detector: detector, nudgeCh: internalNudgeCh}

	var escalated bool
	controlDone := make(chan struct{})
	go func() {
		defer close(controlDone)
		for {
			select {
			case action, ok := <-internalNudgeCh:
				if !ok {
					return
				}
				switch action {
				case loopNudge:
					const loopNudgeText = "You appear to be calling the same tool repeatedly " +
						"with identical arguments. Step back: re-read what you already know, pick a " +
						"different approach, or write your plan now to record your findings so far."
					select {
					case msgCh <- harness.Message{Text: loopNudgeText}:
					case <-childCtx.Done():
						return
					}
				case loopEscalate:
					escalated = true
					cancel()
					return
				}
			case <-childCtx.Done():
				return
			}
		}
	}()

	merged, mergeWait := mergeMessages(childCtx, in, msgCh)

	// When LoopBreaker opens an input plane for nudges, the initial prompt must
	// also go through it. Write it directly into merged's buffer (capacity 8) so
	// it arrives before inner.Run starts — avoids a race where cancel() preempts
	// the pump goroutine on a fast-returning inner executor.
	if in == nil && spec.Prompt != "" {
		merged <- harness.Message{Text: spec.Prompt}
		spec.Prompt = ""
	}

	res, err := e.inner.Run(childCtx, spec, merged, lSink)

	// Shutdown sequence (order matters):
	// 1. close(internalNudgeCh): safe — no more Observe calls after inner.Run returns;
	//    signals the control goroutine to drain remaining actions and exit.
	// 2. <-controlDone: waits for the control goroutine to write any pending nudge to
	//    msgCh and exit. Must precede cancel() so nudges are not dropped.
	// 3. close(msgCh): safe — control goroutine has exited, no more writers to msgCh.
	//    Signals the pump goroutine for msgCh to drain the nudge into merged and exit.
	// 4. cancel(): cancels childCtx, stopping the pump goroutine for the upstream in.
	// 5. mergeWait(): confirms all pump goroutines have exited.
	close(internalNudgeCh)
	<-controlDone
	close(msgCh)
	cancel()
	mergeWait()

	if escalated {
		return res, ErrLoopEscalated
	}
	return res, err
}

// -- SilenceDetector --------------------------------------------------------

type silenceDetector struct{}

// NewSilenceDetector returns a Middleware that sends a nudge when no harness
// events have arrived for spec.SilenceGuard.SilenceSecs seconds. The nudge text
// is spec.SilenceGuard.NudgeText, falling back to spec.PreTimeoutNudge.
// Passthrough when spec.SilenceGuard.SilenceSecs <= 0.
func NewSilenceDetector() Middleware { return silenceDetector{} }

func (silenceDetector) Wrap(inner harness.Executor) harness.Executor {
	return &silenceDetectorExec{inner: inner}
}

type silenceDetectorExec struct{ inner harness.Executor }

// silenceWatcherSink records the timestamp of every observed event.
type silenceWatcherSink struct {
	inner     harness.Sink
	lastEvent atomic.Int64 // Unix ns of last Observe call; 0 = no events yet
}

func (s *silenceWatcherSink) Observe(ev harness.Event) {
	s.lastEvent.Store(time.Now().UnixNano())
	if s.inner != nil {
		s.inner.Observe(ev)
	}
}

func (e *silenceDetectorExec) Run(ctx context.Context, spec harness.ProcessSpec, in <-chan harness.Message, sink harness.Sink) (harness.RunResult, error) {
	if spec.SilenceGuard.SilenceSecs <= 0 {
		return e.inner.Run(ctx, spec, in, sink)
	}

	nudgeText := spec.SilenceGuard.NudgeText
	if nudgeText == "" {
		nudgeText = spec.PreTimeoutNudge
	}

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	msgCh := make(chan harness.Message, 4)
	wSink := &silenceWatcherSink{inner: sink}

	// silenceCtx is a sub-context of childCtx: we stop the sender goroutine
	// (via silenceCancel) before close(msgCh), without cancelling childCtx so
	// the merge pump can still deliver any queued nudges to the inner executor.
	silenceCtx, silenceCancel := context.WithCancel(childCtx)
	defer silenceCancel()

	var sendersDone sync.WaitGroup
	silenceDur := time.Duration(spec.SilenceGuard.SilenceSecs) * time.Second
	startNs := time.Now().UnixNano()
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
				ns := wSink.lastEvent.Load()
				anchor := ns
				if anchor == 0 {
					anchor = startNs // anchor to start when no events have arrived yet
				}
				if time.Since(time.Unix(0, anchor)) >= silenceDur {
					select {
					case msgCh <- harness.Message{Text: nudgeText}:
					default:
					}
				}
			}
		}
	}()

	merged, mergeWait := mergeMessages(childCtx, in, msgCh)

	res, err := e.inner.Run(childCtx, spec, merged, wSink)

	// Shutdown sequence:
	// 1. silenceCancel(): stops the silence goroutine via silenceCtx.
	// 2. sendersDone.Wait(): ensures nobody writes to msgCh after close.
	// 3. close(msgCh): signals the merge pump for msgCh to exit.
	// 4. cancel(): cancels childCtx, causing the merge pump for in to exit.
	// 5. mergeWait(): confirms all pump goroutines have exited.
	silenceCancel()
	sendersDone.Wait()
	close(msgCh)
	cancel()
	mergeWait()

	return res, err
}
