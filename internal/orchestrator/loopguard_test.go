package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/harness"
)

// steeringInner is a fake inner executor for steering tests.
// It receives spec and in, emits scripted events via sink, then signals done.
type steeringInner struct {
	mu       sync.Mutex
	events   []harness.Event
	messages []string // messages received via in channel
	err      error
}

func (f *steeringInner) Run(ctx context.Context, spec harness.ProcessSpec, in <-chan harness.Message, sink harness.Sink) (harness.RunResult, error) {
	// Drain input in background.
	if in != nil {
		go func() {
			for msg := range in {
				f.mu.Lock()
				f.messages = append(f.messages, msg.Text)
				f.mu.Unlock()
			}
		}()
	}
	// Emit scripted events.
	if sink != nil {
		for _, ev := range f.events {
			sink.Observe(ev)
		}
	}
	if f.err != nil {
		return harness.RunResult{}, f.err
	}
	return harness.RunResult{}, nil
}

func (f *steeringInner) receivedMessages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.messages))
	copy(out, f.messages)
	return out
}

// Helper: build a tool-use event.
func toolUseEvent(tool, args string) harness.Event {
	return harness.Event{Kind: harness.EventToolUse, Tool: tool, Args: args}
}

func toolResultEvent(isErr bool) harness.Event {
	return harness.Event{Kind: harness.EventToolResult, IsError: isErr}
}

func sessionDoneEvent() harness.Event {
	return harness.Event{Kind: harness.EventSessionDone}
}

var testLoopGuard = harness.LoopGuardSpec{
	RepeatThreshold: 3,
	MaxNudges:       2,
	CooldownTurns:   1,
}

func TestLoopDetector_NoRepeat(t *testing.T) {
	d := newLoopDetector(testLoopGuard)
	if got := d.observe("Read", `{"path":"/a"}`, false); got != loopNone {
		t.Errorf("distinct tool: want loopNone, got %v", got)
	}
	if got := d.observe("Read", `{"path":"/b"}`, false); got != loopNone {
		t.Errorf("distinct args: want loopNone, got %v", got)
	}
}

func TestLoopDetector_TripsAtThreshold(t *testing.T) {
	d := newLoopDetector(testLoopGuard)
	d.observe("ExitWorktree", `{}`, false) // 1
	d.observe("ExitWorktree", `{}`, false) // 2
	action := d.observe("ExitWorktree", `{}`, false) // 3 = threshold
	if action != loopNudge {
		t.Errorf("expected loopNudge at threshold, got %v", action)
	}
}

func TestLoopDetector_EscalatesAfterMaxNudges(t *testing.T) {
	d := newLoopDetector(testLoopGuard) // threshold=3, maxNudges=2
	for i := 0; i < 3; i++ {
		d.observe("ExitWorktree", `{}`, false)
	}
	// nudge 1

	// cooldown = 1 turn
	d.observe("ExitWorktree", `{}`, false) // cooldown tick, count reset

	// second loop
	for i := 0; i < 3; i++ {
		d.observe("ExitWorktree", `{}`, false)
	}
	// nudge 2 (maxNudges reached)

	// cooldown
	d.observe("ExitWorktree", `{}`, false)

	// third loop → escalate
	for i := 0; i < 2; i++ {
		d.observe("ExitWorktree", `{}`, false)
	}
	action := d.observe("ExitWorktree", `{}`, false)
	if action != loopEscalate {
		t.Errorf("expected loopEscalate, got %v", action)
	}
}

func TestLoopDetector_ErrorResultWeightTwo(t *testing.T) {
	d := newLoopDetector(testLoopGuard) // threshold=3
	d.observe("ExitWorktree", `{}`, true) // weight=2 → count=2
	action := d.observe("ExitWorktree", `{}`, false) // +1 → count=3 = threshold
	if action != loopNudge {
		t.Errorf("expected loopNudge with error weight, got %v", action)
	}
}

func TestSteering_PassthroughWhenDisabled(t *testing.T) {
	inner := &steeringInner{
		events: []harness.Event{sessionDoneEvent()},
	}
	// No LoopGuard, no SilenceGuard, no PreTimeoutNudge → all middleware passthrough.
	exec := NewExecutorBuilder().
		With(NewPreTimeoutNudger()).
		With(NewLoopBreaker()).
		With(NewSilenceDetector()).
		Wrap(inner)

	spec := harness.ProcessSpec{
		Prompt: "hello",
	}
	_, err := exec.Run(context.Background(), spec, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No input plane opened; no messages via in.
	msgs := inner.receivedMessages()
	if len(msgs) != 0 {
		t.Errorf("expected no messages in passthrough mode, got %v", msgs)
	}
}

func TestSteering_PostsInitialPrompt(t *testing.T) {
	inner := &blockingInner{msgNotify: make(chan struct{}, 4)}
	exec := NewExecutorBuilder().With(NewLoopBreaker()).Wrap(inner)

	spec := harness.ProcessSpec{
		Prompt:    "research the codebase",
		LoopGuard: testLoopGuard,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); exec.Run(ctx, spec, nil, nil) }() //nolint:errcheck

	waitForMessage(t, inner, func(m string) bool { return m == "research the codebase" }, "initial prompt")
	cancel()
	<-done
}

func TestSteering_NudgesOnLoop(t *testing.T) {
	// Three identical ExitWorktree calls trip the threshold while the inner is running.
	// The nudge must arrive in the inner's input channel before it exits.
	inner := &blockingInner{
		events: []harness.Event{
			toolUseEvent("ExitWorktree", `{}`), toolResultEvent(false),
			toolUseEvent("ExitWorktree", `{}`), toolResultEvent(false),
			toolUseEvent("ExitWorktree", `{}`), toolResultEvent(false),
		},
		msgNotify: make(chan struct{}, 4),
	}
	exec := NewExecutorBuilder().With(NewLoopBreaker()).Wrap(inner)

	spec := harness.ProcessSpec{
		Prompt:    "work",
		LoopGuard: testLoopGuard,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); exec.Run(ctx, spec, nil, nil) }() //nolint:errcheck

	waitForMessage(t, inner, func(m string) bool {
		return strings.HasPrefix(m, "You appear to be calling")
	}, "loop nudge")
	cancel()
	<-done
}

func TestSteering_EscalatesOnLoopExhaustion(t *testing.T) {
	// Pattern: 3 repeats → nudge 1, cooldown, 3 repeats → nudge 2 (maxNudges=2),
	// cooldown, 3 repeats → escalate. The inner blocks; escalation cancels it for real.
	events := make([]harness.Event, 0, 24)
	addLoop := func() {
		for i := 0; i < 3; i++ {
			events = append(events, toolUseEvent("ExitWorktree", `{}`), toolResultEvent(false))
		}
	}
	addCooldown := func() {
		events = append(events, toolUseEvent("Glob", `{"pattern":"**"}`), toolResultEvent(false))
	}
	addLoop(); addCooldown()
	addLoop(); addCooldown()
	addLoop()

	inner := &blockingInner{events: events}
	exec := NewExecutorBuilder().With(NewLoopBreaker()).Wrap(inner)

	spec := harness.ProcessSpec{
		Prompt:    "work",
		LoopGuard: testLoopGuard,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := exec.Run(ctx, spec, nil, nil)
	if !errors.Is(err, ErrLoopEscalated) {
		t.Errorf("expected ErrLoopEscalated, got %v", err)
	}
}

// blockingInner is a fake executor that blocks until the context is canceled,
// collecting messages from the input channel throughout.
// msgNotify is signaled (non-blocking send) whenever a new message is collected;
// callers use it to avoid polling with time.Sleep.
type blockingInner struct {
	mu        sync.Mutex
	messages  []string
	events    []harness.Event // emitted immediately before blocking
	msgNotify chan struct{}    // non-blocking signal on each new message; nil = no notification
}

func (b *blockingInner) Run(ctx context.Context, spec harness.ProcessSpec, in <-chan harness.Message, sink harness.Sink) (harness.RunResult, error) {
	if in != nil {
		go func() {
			for msg := range in {
				b.mu.Lock()
				b.messages = append(b.messages, msg.Text)
				b.mu.Unlock()
				if b.msgNotify != nil {
					select {
					case b.msgNotify <- struct{}{}:
					default:
					}
				}
			}
		}()
	}
	for _, ev := range b.events {
		sink.Observe(ev)
	}
	<-ctx.Done()
	return harness.RunResult{}, ctx.Err()
}

func (b *blockingInner) received() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.messages))
	copy(out, b.messages)
	return out
}

// waitForMessage blocks until the inner has received a message matching pred,
// or 500 ms passes. Requires b.msgNotify to be set.
// For slow senders (e.g. SilenceDetector with SilenceSecs ≥ 1), use waitForMessageCtx.
func waitForMessage(t *testing.T, b *blockingInner, pred func(string) bool, desc string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	waitForMessageCtx(t, b, ctx, pred, desc)
}

// waitForMessageCtx blocks until pred returns true or ctx is done.
func waitForMessageCtx(t *testing.T, b *blockingInner, ctx context.Context, pred func(string) bool, desc string) {
	t.Helper()
	for {
		for _, m := range b.received() {
			if pred(m) {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Errorf("timed out waiting for message: %s; got %v", desc, b.received())
			return
		case <-b.msgNotify:
		}
	}
}

func TestPreTimeoutNudgeFires(t *testing.T) {
	// With a 200 ms timeout, preTimeoutWarning (60s) > timeout, so warnIn clamps to 0
	// and the nudge fires immediately. The deadline fires shortly after.
	inner := &blockingInner{}
	exec := NewExecutorBuilder().
		With(NewPreTimeoutNudger()).
		With(NewLoopBreaker()).
		Wrap(inner)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	spec := harness.ProcessSpec{
		Prompt:          "do work",
		LoopGuard:       testLoopGuard,
		PreTimeoutNudge: "time is up",
	}
	_, _ = exec.Run(ctx, spec, nil, nil)

	msgs := inner.received()
	hasPrompt, hasNudge := false, false
	for _, m := range msgs {
		if m == "do work" {
			hasPrompt = true
		}
		if m == "time is up" {
			hasNudge = true
		}
	}
	if !hasPrompt {
		t.Errorf("expected prompt in messages; got %v", msgs)
	}
	if !hasNudge {
		t.Errorf("expected pre-timeout nudge in messages; got %v", msgs)
	}
}

func TestPreTimeoutNudgeSkippedOnEarlyExit(t *testing.T) {
	// Inner exits immediately; pre-timeout goroutine must not panic or block.
	inner := &steeringInner{
		events: []harness.Event{sessionDoneEvent()},
	}
	exec := NewExecutorBuilder().
		With(NewPreTimeoutNudger()).
		With(NewLoopBreaker()).
		Wrap(inner)

	// Use a 10-second timeout: nudge would fire at T-60s = negative → immediate,
	// but the inner exits before anyone reads myIn, so the non-blocking send drops it.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	spec := harness.ProcessSpec{
		Prompt:          "work",
		LoopGuard:       testLoopGuard,
		PreTimeoutNudge: "nudge",
	}
	// Should not block or panic.
	done := make(chan struct{})
	go func() {
		defer close(done)
		exec.Run(ctx, spec, nil, nil) //nolint:errcheck
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Run did not return after inner exited")
	}
}

func TestPreTimeoutNudgeNoSteerOnLoop(t *testing.T) {
	// Worker case: no LoopGuard but PreTimeoutNudge set — PreTimeoutNudger is active.
	inner := &blockingInner{}
	exec := NewExecutorBuilder().
		With(NewPreTimeoutNudger()).
		With(NewLoopBreaker()).
		Wrap(inner)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	spec := harness.ProcessSpec{
		Prompt:          "execute plan",
		PreTimeoutNudge: "worker nudge",
	}
	_, _ = exec.Run(ctx, spec, nil, nil)

	msgs := inner.received()
	hasPrompt, hasNudge := false, false
	for _, m := range msgs {
		if m == "execute plan" {
			hasPrompt = true
		}
		if m == "worker nudge" {
			hasNudge = true
		}
	}
	if !hasPrompt {
		t.Errorf("expected prompt to be posted via myIn; got %v", msgs)
	}
	if !hasNudge {
		t.Errorf("expected pre-timeout nudge for worker role; got %v", msgs)
	}
}

func TestPreTimeoutNudgeNoDeadline(t *testing.T) {
	// Context has no deadline: pre-timeout goroutine must not spawn, no panic.
	inner := &steeringInner{
		events: []harness.Event{sessionDoneEvent()},
	}
	exec := NewExecutorBuilder().
		With(NewPreTimeoutNudger()).
		With(NewLoopBreaker()).
		Wrap(inner)

	spec := harness.ProcessSpec{
		Prompt:          "work",
		LoopGuard:       testLoopGuard,
		PreTimeoutNudge: "nudge",
	}
	// context.Background() has no deadline.
	_, err := exec.Run(context.Background(), spec, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSilenceNudge(t *testing.T) {
	// After one event, silence for > SilenceSecs → SilenceDetector fires the nudge.
	// Only SilenceDetector in the chain; no PreTimeoutNudger so "please submit" can
	// only arrive from silence detection (not from a zero-delay pre-timeout goroutine).
	inner := &blockingInner{
		events: []harness.Event{
			toolUseEvent("Read", `{"path":"/a"}`), // seeds lastEvent
		},
		msgNotify: make(chan struct{}, 8),
	}
	exec := NewExecutorBuilder().With(NewSilenceDetector()).Wrap(inner)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	spec := harness.ProcessSpec{
		Prompt:          "work",
		SilenceGuard:    harness.SilenceGuardSpec{SilenceSecs: 1, NudgeText: "please submit"},
	}

	done := make(chan struct{})
	go func() { defer close(done); exec.Run(ctx, spec, nil, nil) }() //nolint:errcheck

	waitForMessageCtx(t, inner, ctx, func(m string) bool { return m == "please submit" }, "silence nudge")

	cancel()
	<-done
}

func TestSilenceNudgeFiresOnTotalSilence(t *testing.T) {
	// No events at all → silence clock anchors to start time → nudge fires after SilenceSecs.
	// Only SilenceDetector in the chain so the nudge can only come from silence detection.
	inner := &blockingInner{
		msgNotify: make(chan struct{}, 8),
	}
	exec := NewExecutorBuilder().With(NewSilenceDetector()).Wrap(inner)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	spec := harness.ProcessSpec{
		Prompt:       "work",
		SilenceGuard: harness.SilenceGuardSpec{SilenceSecs: 1, NudgeText: "please submit"},
	}

	done := make(chan struct{})
	go func() { defer close(done); exec.Run(ctx, spec, nil, nil) }() //nolint:errcheck

	waitForMessageCtx(t, inner, ctx, func(m string) bool { return m == "please submit" }, "silence nudge on total silence")

	cancel()
	<-done
}

func TestSilenceNudge_UsesNudgeText(t *testing.T) {
	// When SilenceGuard.NudgeText is set, it must be used instead of PreTimeoutNudge.
	inner := &blockingInner{
		events:    []harness.Event{toolUseEvent("Read", `{"path":"/a"}`)},
		msgNotify: make(chan struct{}, 8),
	}
	exec := NewExecutorBuilder().With(NewSilenceDetector()).Wrap(inner)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	spec := harness.ProcessSpec{
		Prompt:          "work",
		SilenceGuard:    harness.SilenceGuardSpec{SilenceSecs: 1, NudgeText: "custom nudge"},
		PreTimeoutNudge: "fallback",
	}

	done := make(chan struct{})
	go func() { defer close(done); exec.Run(ctx, spec, nil, nil) }() //nolint:errcheck

	waitForMessageCtx(t, inner, ctx, func(m string) bool { return m == "custom nudge" }, "custom NudgeText")

	for _, m := range inner.received() {
		if m == "fallback" {
			t.Errorf("PreTimeoutNudge fallback appeared; SilenceGuard.NudgeText should take precedence")
		}
	}

	cancel()
	<-done
}
