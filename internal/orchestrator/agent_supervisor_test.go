package orchestrator

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/harness"
)

// --- fakes -------------------------------------------------------------------

// syncInner blocks until its context is done, collecting input messages.
// Emits scripted events before blocking.
type syncInner struct {
	mu        sync.Mutex
	messages  []string
	events    []harness.Event
	msgNotify chan struct{}
	result    harness.RunResult
	err       error
}

func (b *syncInner) Run(ctx context.Context, _ harness.ProcessSpec, in <-chan harness.Message, sink harness.Sink) (harness.RunResult, error) {
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
	if sink != nil {
		for _, ev := range b.events {
			sink.Observe(ev)
		}
	}
	if b.err != nil {
		return b.result, b.err
	}
	<-ctx.Done()
	return b.result, ctx.Err()
}

func (b *syncInner) received() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.messages))
	copy(out, b.messages)
	return out
}

// immediateInner returns immediately (base executor fast-exit).
type immediateInner struct {
	result harness.RunResult
	err    error
}

func (i *immediateInner) Run(_ context.Context, _ harness.ProcessSpec, _ <-chan harness.Message, _ harness.Sink) (harness.RunResult, error) {
	return i.result, i.err
}

// preFiredSignaler returns a pre-closed channel for all agentIDs.
type preFiredSignaler struct{}

func (preFiredSignaler) ReportSignal(_ string) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// --- helpers -----------------------------------------------------------------

func newTestSupervisor(base harness.Executor) *AgentSupervisor {
	guard := NewBudgetGuard(NewRunUsage(0)) // unlimited
	return NewAgentSupervisor(base, nil, guard)
}

func waitFor(t *testing.T, b *syncInner, pred func(string) bool, desc string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	for {
		if slices.ContainsFunc(b.received(), pred) {
			return
		}
		select {
		case <-ctx.Done():
			t.Errorf("timed out waiting for %s; got %v", desc, b.received())
			return
		case <-b.msgNotify:
		}
	}
}

// --- tests -------------------------------------------------------------------

func TestSupervisor_NormalExit(t *testing.T) {
	inner := &immediateInner{result: harness.RunResult{Output: "done"}}
	sup := newTestSupervisor(inner)

	res, err := sup.Run(context.Background(), harness.ProcessSpec{Prompt: "go"}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Output != "done" {
		t.Errorf("expected output 'done', got %q", res.Output)
	}
}

func TestSupervisor_Passthrough_NoPolicies(t *testing.T) {
	// No policies, no in — should behave identically to base executor.
	inner := &immediateInner{}
	sup := newTestSupervisor(inner)

	_, err := sup.Run(context.Background(), harness.ProcessSpec{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSupervisor_TimeoutStop(t *testing.T) {
	inner := &syncInner{}
	sup := newTestSupervisor(inner)

	spec := harness.ProcessSpec{
		Timeout: 50 * time.Millisecond,
		Prompt:  "work",
		LoopGuard: harness.LoopGuardSpec{
			RepeatThreshold: 3, MaxNudges: 1, CooldownTurns: 1,
		},
	}

	_, err := sup.Run(context.Background(), spec, nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded on timeout, got %v", err)
	}
}

func TestSupervisor_ParentCancelPropagates(t *testing.T) {
	inner := &syncInner{}
	sup := newTestSupervisor(inner)

	ctx, cancel := context.WithCancel(context.Background())

	spec := harness.ProcessSpec{
		Prompt:    "work",
		LoopGuard: harness.LoopGuardSpec{RepeatThreshold: 3, MaxNudges: 1, CooldownTurns: 1},
	}

	done := make(chan error, 1)
	go func() {
		_, err := sup.Run(ctx, spec, nil, nil)
		done <- err
	}()

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Run did not return after parent cancel")
	}
}

func TestSupervisor_LoopEscalation(t *testing.T) {
	// Send enough identical tool calls to trip escalation.
	events := []harness.Event{
		{Kind: harness.EventToolUse, Tool: "ExitWorktree", Args: "{}"},
		{Kind: harness.EventToolResult},
		{Kind: harness.EventToolUse, Tool: "ExitWorktree", Args: "{}"},
		{Kind: harness.EventToolResult},
		{Kind: harness.EventToolUse, Tool: "ExitWorktree", Args: "{}"},
		{Kind: harness.EventToolResult},
		// Cooldown turn
		{Kind: harness.EventToolUse, Tool: "Glob", Args: "{}"},
		{Kind: harness.EventToolResult},
		// Second loop → escalate (maxNudges=1 already hit)
		{Kind: harness.EventToolUse, Tool: "ExitWorktree", Args: "{}"},
		{Kind: harness.EventToolResult},
		{Kind: harness.EventToolUse, Tool: "ExitWorktree", Args: "{}"},
		{Kind: harness.EventToolResult},
		{Kind: harness.EventToolUse, Tool: "ExitWorktree", Args: "{}"},
		{Kind: harness.EventToolResult},
	}
	inner := &syncInner{events: events}
	sup := newTestSupervisor(inner)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	spec := harness.ProcessSpec{
		Prompt: "work",
		LoopGuard: harness.LoopGuardSpec{
			RepeatThreshold: 3, MaxNudges: 1, CooldownTurns: 1,
		},
	}

	_, err := sup.Run(ctx, spec, nil, nil)
	if !errors.Is(err, ErrLoopEscalated) {
		t.Errorf("expected ErrLoopEscalated, got %v", err)
	}
}

func TestSupervisor_ReportArrivalStopsRun(t *testing.T) {
	inner := &syncInner{}
	guard := NewBudgetGuard(NewRunUsage(0))
	sup := NewAgentSupervisor(inner, preFiredSignaler{}, guard)

	spec := harness.ProcessSpec{
		AgentID:       "architect",
		ExpectsReport: true,
		Prompt:        "plan it",
		LoopGuard:     harness.LoopGuardSpec{RepeatThreshold: 3, MaxNudges: 1, CooldownTurns: 1},
	}

	done := make(chan error, 1)
	go func() {
		_, err := sup.Run(context.Background(), spec, nil, nil)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("report-arrival stop: expected nil error, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Run did not return after report signal fired")
	}
}

func TestSupervisor_BudgetPreCheck(t *testing.T) {
	inner := &immediateInner{}
	u := NewRunUsage(100)
	u.Record("prev", 60, 60) // over budget
	guard := NewBudgetGuard(u)
	sup := NewAgentSupervisor(inner, nil, guard)

	_, err := sup.Run(context.Background(), harness.ProcessSpec{}, nil, nil)
	if !errors.Is(err, harness.ErrBudgetExhausted) {
		t.Errorf("expected ErrBudgetExhausted, got %v", err)
	}
}

func TestSupervisor_NudgeSentOnLoop(t *testing.T) {
	inner := &syncInner{
		msgNotify: make(chan struct{}, 8),
		events: []harness.Event{
			{Kind: harness.EventToolUse, Tool: "ExitWorktree", Args: "{}"},
			{Kind: harness.EventToolResult},
			{Kind: harness.EventToolUse, Tool: "ExitWorktree", Args: "{}"},
			{Kind: harness.EventToolResult},
			{Kind: harness.EventToolUse, Tool: "ExitWorktree", Args: "{}"},
			{Kind: harness.EventToolResult},
		},
	}
	sup := newTestSupervisor(inner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	spec := harness.ProcessSpec{
		Prompt: "work",
		LoopGuard: harness.LoopGuardSpec{
			RepeatThreshold: 3, MaxNudges: 3, CooldownTurns: 2,
		},
	}

	go func() { sup.Run(ctx, spec, nil, nil) }() //nolint:errcheck

	waitFor(t, inner, func(m string) bool {
		return m == "work" // initial prompt seeded into msgs
	}, "initial prompt in msgs")

	waitFor(t, inner, func(m string) bool {
		return m == loopNudgeText
	}, "loop nudge message")

	cancel()
}
