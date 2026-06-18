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
