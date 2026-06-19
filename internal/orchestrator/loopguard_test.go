package orchestrator

import (
	"context"
	"errors"
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
	s := NewSteeringExecutor(inner)

	spec := harness.ProcessSpec{
		SteerOnLoop: false,
		Prompt:      "hello",
	}
	_, err := s.Run(context.Background(), spec, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// no messages should arrive (no input plane opened)
	msgs := inner.receivedMessages()
	if len(msgs) != 0 {
		t.Errorf("expected no messages in passthrough mode, got %v", msgs)
	}
}

func TestSteering_PostsInitialPrompt(t *testing.T) {
	inner := &steeringInner{
		events: []harness.Event{sessionDoneEvent()},
	}
	s := NewSteeringExecutor(inner)

	spec := harness.ProcessSpec{
		SteerOnLoop: true,
		Prompt:      "research the codebase",
		LoopGuard:   testLoopGuard,
	}
	_, err := s.Run(context.Background(), spec, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Give the drain goroutine a moment.
	time.Sleep(10 * time.Millisecond)

	msgs := inner.receivedMessages()
	if len(msgs) == 0 {
		t.Fatal("expected initial prompt to be posted as first message")
	}
	if msgs[0] != "research the codebase" {
		t.Errorf("first message = %q, want 'research the codebase'", msgs[0])
	}
	// -p should have been blanked
	if spec.Prompt != "research the codebase" {
		// spec is a value — test that our local copy was updated
	}
}

func TestSteering_NudgesOnLoop(t *testing.T) {
	// Emit 3 identical ExitWorktree calls → expect nudge message.
	events := []harness.Event{
		toolUseEvent("ExitWorktree", `{}`),
		toolResultEvent(false),
		toolUseEvent("ExitWorktree", `{}`),
		toolResultEvent(false),
		toolUseEvent("ExitWorktree", `{}`),
		toolResultEvent(false),
		sessionDoneEvent(),
	}
	inner := &steeringInner{events: events}
	s := NewSteeringExecutor(inner)

	spec := harness.ProcessSpec{
		SteerOnLoop: true,
		Prompt:      "work",
		LoopGuard:   testLoopGuard,
	}
	_, err := s.Run(context.Background(), spec, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Wait for controller goroutine to flush.
	time.Sleep(20 * time.Millisecond)

	msgs := inner.receivedMessages()
	hasNudge := false
	for _, m := range msgs {
		if len(m) > 20 && m[:20] == "You appear to be cal" {
			hasNudge = true
		}
	}
	if !hasNudge {
		t.Errorf("expected nudge message in %v", msgs)
	}
}

func TestSteering_EscalatesOnLoopExhaustion(t *testing.T) {
	// Emit enough repeated calls to exhaust nudges (threshold=3, maxNudges=2).
	// Pattern: 3 repeats → nudge, cooldown, 3 repeats → nudge, cooldown, 3 repeats → escalate.
	events := make([]harness.Event, 0, 30)
	addLoop := func(n int) {
		for i := 0; i < n; i++ {
			events = append(events, toolUseEvent("ExitWorktree", `{}`))
			events = append(events, toolResultEvent(true))
		}
	}
	addLoop(3) // nudge 1 — but with errResult weight=2, threshold=3 needs 2 calls
	// Actually with errResult weight=2 and threshold=3:
	// call1: count=2, call2: count=4 ≥ 3 → nudge. So 2 calls with error suffice.
	// Let's use simple non-error calls for clarity.

	events = events[:0]
	// loop 1 (3 calls → nudge 1)
	for i := 0; i < 3; i++ {
		events = append(events, toolUseEvent("ExitWorktree", `{}`), toolResultEvent(false))
	}
	// cooldown: 1 different call
	events = append(events, toolUseEvent("Glob", `{"pattern":"**"}`), toolResultEvent(false))
	// loop 2 (3 calls → nudge 2)
	for i := 0; i < 3; i++ {
		events = append(events, toolUseEvent("ExitWorktree", `{}`), toolResultEvent(false))
	}
	// cooldown: 1 different call
	events = append(events, toolUseEvent("Glob", `{"pattern":"**"}`), toolResultEvent(false))
	// loop 3 (3 calls → escalate, maxNudges exhausted)
	for i := 0; i < 3; i++ {
		events = append(events, toolUseEvent("ExitWorktree", `{}`), toolResultEvent(false))
	}
	// no session done — escalation cancels before it

	inner := &steeringInner{events: events, err: context.Canceled}
	s := NewSteeringExecutor(inner)

	spec := harness.ProcessSpec{
		SteerOnLoop: true,
		Prompt:      "work",
		LoopGuard:   testLoopGuard,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := s.Run(ctx, spec, nil, nil)
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
// or a 500 ms deadline passes. Requires b.msgNotify to be set.
func waitForMessage(t *testing.T, b *blockingInner, pred func(string) bool, desc string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
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
	s := NewSteeringExecutor(inner)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	spec := harness.ProcessSpec{
		SteerOnLoop:    true,
		Prompt:         "do work",
		LoopGuard:      testLoopGuard,
		PreTimeoutNudge: "time is up",
	}
	_, _ = s.Run(ctx, spec, nil, nil)

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
	s := NewSteeringExecutor(inner)

	// Use a 10-second timeout: nudge would fire at T-60s = negative → immediate,
	// but the inner exits before anyone reads myIn, so the non-blocking send drops it.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	spec := harness.ProcessSpec{
		SteerOnLoop:    true,
		Prompt:         "work",
		LoopGuard:      testLoopGuard,
		PreTimeoutNudge: "nudge",
	}
	// Should not block or panic.
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx, spec, nil, nil) //nolint:errcheck
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Run did not return after inner exited")
	}
}

func TestPreTimeoutNudgeNoSteerOnLoop(t *testing.T) {
	// Worker case: SteerOnLoop=false but PreTimeoutNudge set — enters steerable path.
	inner := &blockingInner{}
	s := NewSteeringExecutor(inner)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	spec := harness.ProcessSpec{
		SteerOnLoop:    false,
		Prompt:         "execute plan",
		PreTimeoutNudge: "worker nudge",
	}
	_, _ = s.Run(ctx, spec, nil, nil)

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
	s := NewSteeringExecutor(inner)

	spec := harness.ProcessSpec{
		SteerOnLoop:    true,
		Prompt:         "work",
		LoopGuard:      testLoopGuard,
		PreTimeoutNudge: "nudge",
	}
	// context.Background() has no deadline.
	_, err := s.Run(context.Background(), spec, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSilenceNudge(t *testing.T) {
	// After one event, go silent for > SilenceSecs → nudge fires.
	// SilenceSecs=1 means silence goroutine checks every 500ms and fires after 1s quiet.
	inner := &blockingInner{
		events: []harness.Event{
			toolUseEvent("Read", `{"path":"/a"}`), // seeds lastEvent
		},
		msgNotify: make(chan struct{}, 8),
	}
	s := NewSteeringExecutor(inner)

	// 3-second deadline so the silence nudge (at ~1s) fires before the ctx deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	spec := harness.ProcessSpec{
		SteerOnLoop:    true,
		Prompt:         "work",
		LoopGuard:      harness.LoopGuardSpec{RepeatThreshold: 3, MaxNudges: 3, CooldownTurns: 1, SilenceSecs: 1},
		PreTimeoutNudge: "please submit",
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx, spec, nil, nil) //nolint:errcheck
	}()

	// Wait for silence nudge to arrive (up to 2s after start).
	waitForMessage(t, inner, func(m string) bool { return m == "please submit" }, "silence nudge")

	cancel() // allow Run to return
	<-done
}

func TestSilenceNudgeSkippedBeforeFirstEvent(t *testing.T) {
	// No events from inner → lastEvent is zero → silence goroutine must not fire.
	inner := &blockingInner{} // no events
	s := NewSteeringExecutor(inner)

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	spec := harness.ProcessSpec{
		SteerOnLoop: true,
		Prompt:      "work",
		LoopGuard:   harness.LoopGuardSpec{RepeatThreshold: 3, MaxNudges: 3, CooldownTurns: 1, SilenceSecs: 1},
	}
	_, _ = s.Run(ctx, spec, nil, nil)

	for _, m := range inner.received() {
		if m != "work" { // prompt is expected; anything else is a spurious nudge
			t.Errorf("spurious message when no events emitted: %q", m)
		}
	}
}
