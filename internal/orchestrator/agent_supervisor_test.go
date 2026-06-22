package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/harness"
)

// --- compliant executors -----------------------------------------------------
// All wrap fixturePlayer (real JSONL replay) instead of hand-writing events.
//
// blockingPlayer: replays fixture then blocks on ctx.Done — for tests where
// the supervisor must cancel a running executor (timeout, parent cancel, report).
//
// blockingCapturingPlayer: replays fixture, records inbound messages, then blocks —
// for TestSupervisor_NudgeSentOnLoop, which asserts the supervisor writes a
// nudge back to the executor's input channel.

type blockingPlayer struct{ player *fixturePlayer }

func (b *blockingPlayer) Run(ctx context.Context, spec harness.ProcessSpec, in <-chan harness.Message, sink harness.Sink) (harness.RunResult, error) {
	res, err := b.player.Run(ctx, spec, in, sink)
	if err != nil {
		return res, err
	}
	<-ctx.Done()
	return res, ctx.Err()
}

type blockingCapturingPlayer struct {
	player   *fixturePlayer
	mu       sync.Mutex
	messages []string
	notify   chan struct{}
}

func newBlockingCapturingPlayer(path string) *blockingCapturingPlayer {
	return &blockingCapturingPlayer{
		player: &fixturePlayer{path: path},
		notify: make(chan struct{}, 16),
	}
}

func (c *blockingCapturingPlayer) Run(ctx context.Context, spec harness.ProcessSpec, in <-chan harness.Message, sink harness.Sink) (harness.RunResult, error) {
	if in != nil {
		go func() {
			for msg := range in {
				c.mu.Lock()
				c.messages = append(c.messages, msg.Text)
				c.mu.Unlock()
				select {
				case c.notify <- struct{}{}:
				default:
				}
			}
		}()
	}
	// Pass nil for in: the capturing goroutine above drains it; the player
	// replays from file only.
	res, err := c.player.Run(ctx, spec, nil, sink)
	if err != nil {
		return res, err
	}
	<-ctx.Done()
	return res, ctx.Err()
}

func (c *blockingCapturingPlayer) hasMessage(text string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range c.messages {
		if m == text {
			return true
		}
	}
	return false
}

// --- other helpers -----------------------------------------------------------

// preFiredSignaler returns a pre-closed channel for all agentIDs.
type preFiredSignaler struct{}

func (preFiredSignaler) ReportSignal(_ string) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func newTestSupervisor(base harness.Executor) *AgentSupervisor {
	guard := NewBudgetGuard(NewRunUsage(0)) // unlimited
	return NewAgentSupervisor(base, nil, guard)
}

// waitForMsg polls pred until it returns true or timeout expires.
func waitForMsg(t *testing.T, pred func() bool, timeout time.Duration, desc string) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		if pred() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for: %s", desc)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// --- tests -------------------------------------------------------------------

func TestSupervisor_NormalExit(t *testing.T) {
	// fixturePlayer returns RunResult{} (Output==""); assert clean exit with no error.
	player := &fixturePlayer{path: "testdata/normal_exit.jsonl"}
	sup := newTestSupervisor(player)

	_, err := sup.Run(context.Background(), harness.ProcessSpec{Prompt: "go"}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSupervisor_Passthrough_NoPolicies(t *testing.T) {
	// No policies, no in — should behave identically to base executor.
	player := &fixturePlayer{path: "testdata/normal_exit.jsonl"}
	sup := newTestSupervisor(player)

	_, err := sup.Run(context.Background(), harness.ProcessSpec{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSupervisor_TimeoutStop(t *testing.T) {
	// blockingPlayer: fixture completes instantly, then blocks on ctx.Done.
	// Timeout fires → ctx cancelled → blockingPlayer returns DeadlineExceeded.
	player := &blockingPlayer{player: &fixturePlayer{path: "testdata/normal_exit.jsonl"}}
	sup := newTestSupervisor(player)

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
	player := &blockingPlayer{player: &fixturePlayer{path: "testdata/normal_exit.jsonl"}}
	sup := newTestSupervisor(player)

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
	// exitworktree_loop.jsonl has 31 ExitWorktree calls — enough to trip escalation
	// with RepeatThreshold:3, MaxNudges:1.
	player := &fixturePlayer{path: "testdata/exitworktree_loop.jsonl"}
	sup := newTestSupervisor(player)

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
	// preFiredSignaler returns a pre-closed channel — report "already arrived".
	// Supervisor detects it and stops cleanly (errReportArrived → nil).
	player := &blockingPlayer{player: &fixturePlayer{path: "testdata/normal_exit.jsonl"}}
	guard := NewBudgetGuard(NewRunUsage(0))
	sup := NewAgentSupervisor(player, preFiredSignaler{}, guard)

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
	// Budget check runs before the executor — fixturePlayer is never called.
	player := &fixturePlayer{path: "testdata/normal_exit.jsonl"}
	u := NewRunUsage(100)
	u.Record("prev", 60, 60) // over budget
	guard := NewBudgetGuard(u)
	sup := NewAgentSupervisor(player, nil, guard)

	_, err := sup.Run(context.Background(), harness.ProcessSpec{}, nil, nil)
	if !errors.Is(err, harness.ErrBudgetExhausted) {
		t.Errorf("expected ErrBudgetExhausted, got %v", err)
	}
}

func TestSupervisor_NudgeSentOnLoop(t *testing.T) {
	// exitworktree_loop.jsonl triggers the loop guard; supervisor writes loopNudgeText
	// to the executor's input channel. blockingCapturingPlayer records it.
	cp := newBlockingCapturingPlayer("testdata/exitworktree_loop.jsonl")
	sup := newTestSupervisor(cp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	spec := harness.ProcessSpec{
		Prompt: "work",
		LoopGuard: harness.LoopGuardSpec{
			RepeatThreshold: 3, MaxNudges: 3, CooldownTurns: 2,
		},
	}

	go func() { sup.Run(ctx, spec, nil, nil) }() //nolint:errcheck

	waitForMsg(t, func() bool { return cp.hasMessage(loopNudgeText) }, 2*time.Second, "loop nudge message")
	cancel()
}
