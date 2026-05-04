package tokenlimit

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
)

// mockCLIRunner implements harness.CLIRunner for testing.
type mockCLIRunner struct {
	runPrintFn     func(ctx context.Context, prompt, systemPrompt string) (harness.RunResult, error)
	runStreamingFn func(ctx context.Context, prompt, systemPrompt string, stdout io.Writer) (harness.RunResult, error)
}

func (m *mockCLIRunner) RunPrint(ctx context.Context, prompt, systemPrompt string) (harness.RunResult, error) {
	if m.runPrintFn != nil {
		return m.runPrintFn(ctx, prompt, systemPrompt)
	}
	return harness.RunResult{}, nil
}

func (m *mockCLIRunner) RunStreaming(ctx context.Context, prompt, systemPrompt string, stdout io.Writer) (harness.RunResult, error) {
	if m.runStreamingFn != nil {
		return m.runStreamingFn(ctx, prompt, systemPrompt, stdout)
	}
	return harness.RunResult{}, nil
}

func newTestLimiter(t *testing.T, limits map[string]int64) *Limiter {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "tokens.db"))
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return NewLimiter(store, limits)
}

// --- LimitedRunner.RunPrint ---

func TestLimitedRunner_RunPrint_BudgetExhaustedBlocks(t *testing.T) {
	called := false
	inner := &mockCLIRunner{
		runPrintFn: func(_ context.Context, _, _ string) (harness.RunResult, error) {
			called = true
			return harness.RunResult{}, nil
		},
	}
	limiter := newTestLimiter(t, map[string]int64{"opus": 100})
	// Pre-exhaust the budget.
	limiter.store.Record(context.Background(), "opus", "other", 200)

	runner := NewLimitedRunner(inner, limiter, "opus", "planner")
	_, err := runner.RunPrint(context.Background(), "prompt", "sys")
	if !IsBudgetExhausted(err) {
		t.Fatalf("expected ErrBudgetExhausted, got %v", err)
	}
	if called {
		t.Error("inner runner must not be called when budget is exhausted")
	}
}

func TestLimitedRunner_RunPrint_PassthroughOnBudgetOK(t *testing.T) {
	inner := &mockCLIRunner{
		runPrintFn: func(_ context.Context, _, _ string) (harness.RunResult, error) {
			return harness.RunResult{Output: "the answer"}, nil
		},
	}
	limiter := newTestLimiter(t, map[string]int64{"opus": 10000})
	runner := NewLimitedRunner(inner, limiter, "opus", "planner")

	result, err := runner.RunPrint(context.Background(), "prompt", "sys")
	if err != nil {
		t.Fatalf("RunPrint() error: %v", err)
	}
	if result.Output != "the answer" {
		t.Errorf("output = %q, want %q", result.Output, "the answer")
	}
}

func TestLimitedRunner_RunPrint_PostRecordBudgetError(t *testing.T) {
	inner := &mockCLIRunner{
		runPrintFn: func(_ context.Context, _, _ string) (harness.RunResult, error) {
			return harness.RunResult{
				Output: "done",
				Usage:  &harness.TokenUsage{TotalTokens: 900},
			}, nil
		},
	}
	// Limit 1000, already used 500 — adding 900 will exceed.
	limiter := newTestLimiter(t, map[string]int64{"opus": 1000})
	limiter.store.Record(context.Background(), "opus", "prior", 500)

	runner := NewLimitedRunner(inner, limiter, "opus", "planner")
	result, err := runner.RunPrint(context.Background(), "prompt", "sys")

	// Result output must still be returned even alongside the budget error.
	if result.Output != "done" {
		t.Errorf("output = %q, want %q", result.Output, "done")
	}
	if !IsBudgetExhausted(err) {
		t.Fatalf("expected ErrBudgetExhausted, got %v", err)
	}
}

func TestLimitedRunner_RunPrint_ZeroTokensSkipsRecord(t *testing.T) {
	inner := &mockCLIRunner{
		runPrintFn: func(_ context.Context, _, _ string) (harness.RunResult, error) {
			return harness.RunResult{
				Usage: &harness.TokenUsage{TotalTokens: 0},
			}, nil
		},
	}
	limiter := newTestLimiter(t, map[string]int64{"opus": 1000})
	runner := NewLimitedRunner(inner, limiter, "opus", "planner")

	_, err := runner.RunPrint(context.Background(), "prompt", "sys")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	used, _ := limiter.store.UsageByModel(context.Background(), "opus")
	if used != 0 {
		t.Errorf("expected 0 tokens recorded, got %d", used)
	}
}

func TestLimitedRunner_RunPrint_RecordsOnInnerError(t *testing.T) {
	innerErr := errors.New("execution failed")
	inner := &mockCLIRunner{
		runPrintFn: func(_ context.Context, _, _ string) (harness.RunResult, error) {
			return harness.RunResult{
				Usage: &harness.TokenUsage{TotalTokens: 300},
			}, innerErr
		},
	}
	limiter := newTestLimiter(t, map[string]int64{"opus": 10000})
	runner := NewLimitedRunner(inner, limiter, "opus", "planner")

	_, err := runner.RunPrint(context.Background(), "prompt", "sys")
	if !errors.Is(err, innerErr) {
		t.Fatalf("expected innerErr, got %v", err)
	}
	// Tokens must be recorded even though inner errored.
	used, _ := limiter.store.UsageByModel(context.Background(), "opus")
	if used != 300 {
		t.Errorf("expected 300 tokens recorded, got %d", used)
	}
}

func TestLimitedRunner_RunPrint_NilUsageSkipsRecord(t *testing.T) {
	inner := &mockCLIRunner{
		runPrintFn: func(_ context.Context, _, _ string) (harness.RunResult, error) {
			return harness.RunResult{Usage: nil}, nil
		},
	}
	limiter := newTestLimiter(t, map[string]int64{"opus": 1000})
	runner := NewLimitedRunner(inner, limiter, "opus", "planner")

	_, err := runner.RunPrint(context.Background(), "prompt", "sys")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	used, _ := limiter.store.UsageByModel(context.Background(), "opus")
	if used != 0 {
		t.Errorf("expected 0 tokens, got %d", used)
	}
}

func TestLimitedRunner_RunPrint_NoLimit_AlwaysPasses(t *testing.T) {
	inner := &mockCLIRunner{
		runPrintFn: func(_ context.Context, _, _ string) (harness.RunResult, error) {
			return harness.RunResult{
				Output: "ok",
				Usage:  &harness.TokenUsage{TotalTokens: 999999},
			}, nil
		},
	}
	// No limit configured for "opus".
	limiter := newTestLimiter(t, map[string]int64{})
	runner := NewLimitedRunner(inner, limiter, "opus", "planner")

	result, err := runner.RunPrint(context.Background(), "prompt", "sys")
	if err != nil {
		t.Fatalf("expected no error with unlimited model, got %v", err)
	}
	if result.Output != "ok" {
		t.Errorf("output = %q, want %q", result.Output, "ok")
	}
}

// --- LimitedRunner.RunStreaming ---

func TestLimitedRunner_RunStreaming_BudgetExhaustedBlocks(t *testing.T) {
	called := false
	inner := &mockCLIRunner{
		runStreamingFn: func(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
			called = true
			return harness.RunResult{}, nil
		},
	}
	limiter := newTestLimiter(t, map[string]int64{"opus": 100})
	limiter.store.Record(context.Background(), "opus", "other", 200)

	runner := NewLimitedRunner(inner, limiter, "opus", "worker")
	_, err := runner.RunStreaming(context.Background(), "prompt", "sys", io.Discard)
	if !IsBudgetExhausted(err) {
		t.Fatalf("expected ErrBudgetExhausted, got %v", err)
	}
	if called {
		t.Error("inner must not be called when budget exhausted")
	}
}

func TestLimitedRunner_RunStreaming_RecordsOnInnerError(t *testing.T) {
	innerErr := errors.New("stream failed")
	inner := &mockCLIRunner{
		runStreamingFn: func(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
			return harness.RunResult{
				Usage: &harness.TokenUsage{TotalTokens: 150},
			}, innerErr
		},
	}
	limiter := newTestLimiter(t, map[string]int64{"opus": 10000})
	runner := NewLimitedRunner(inner, limiter, "opus", "worker")

	_, err := runner.RunStreaming(context.Background(), "prompt", "sys", io.Discard)
	if !errors.Is(err, innerErr) {
		t.Fatalf("expected innerErr, got %v", err)
	}
	used, _ := limiter.store.UsageByModel(context.Background(), "opus")
	if used != 150 {
		t.Errorf("expected 150 tokens recorded, got %d", used)
	}
}

func TestLimitedRunner_RunStreaming_PostRecordBudgetError(t *testing.T) {
	inner := &mockCLIRunner{
		runStreamingFn: func(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
			return harness.RunResult{
				Output: "streamed",
				Usage:  &harness.TokenUsage{TotalTokens: 600},
			}, nil
		},
	}
	limiter := newTestLimiter(t, map[string]int64{"opus": 1000})
	limiter.store.Record(context.Background(), "opus", "prior", 500)

	runner := NewLimitedRunner(inner, limiter, "opus", "worker")
	result, err := runner.RunStreaming(context.Background(), "prompt", "sys", io.Discard)

	if result.Output != "streamed" {
		t.Errorf("output = %q, want %q", result.Output, "streamed")
	}
	if !IsBudgetExhausted(err) {
		t.Fatalf("expected ErrBudgetExhausted, got %v", err)
	}
}

// --- ErrBudgetExhausted ---

func TestErrBudgetExhausted_ErrorFormat(t *testing.T) {
	err := &ErrBudgetExhausted{
		Model:   "claude-opus-4-5",
		AgentID: "planner",
		Used:    1500,
		Limit:   1000,
	}
	msg := err.Error()
	for _, want := range []string{"claude-opus-4-5", "planner", "1500", "1000"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q missing %q", msg, want)
		}
	}
}

// --- Limiter.StatusAll ---

func TestLimiter_StatusAll(t *testing.T) {
	limiter := newTestLimiter(t, map[string]int64{
		"opus":   10000,
		"sonnet": 5000,
	})
	ctx := context.Background()
	limiter.store.Record(ctx, "opus", "planner", 2000)
	limiter.store.Record(ctx, "sonnet", "validator", 1000)

	statuses, err := limiter.StatusAll(ctx)
	if err != nil {
		t.Fatalf("StatusAll() error: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}

	byModel := make(map[string]ModelStatus, len(statuses))
	for _, s := range statuses {
		byModel[s.Model] = s
	}

	opus := byModel["opus"]
	if opus.Used != 2000 {
		t.Errorf("opus used = %d, want 2000", opus.Used)
	}
	if opus.Remaining != 8000 {
		t.Errorf("opus remaining = %d, want 8000", opus.Remaining)
	}

	sonnet := byModel["sonnet"]
	if sonnet.Used != 1000 {
		t.Errorf("sonnet used = %d, want 1000", sonnet.Used)
	}
	if sonnet.Remaining != 4000 {
		t.Errorf("sonnet remaining = %d, want 4000", sonnet.Remaining)
	}
}

func TestLimiter_StatusAll_Empty(t *testing.T) {
	limiter := newTestLimiter(t, map[string]int64{})
	statuses, err := limiter.StatusAll(context.Background())
	if err != nil {
		t.Fatalf("StatusAll() error: %v", err)
	}
	if len(statuses) != 0 {
		t.Errorf("expected 0 statuses for empty limits, got %d", len(statuses))
	}
}

// --- Limiter.Record edge: tokens <= 0 ---

func TestLimiter_RecordZeroTokens_NoWrite(t *testing.T) {
	limiter := newTestLimiter(t, map[string]int64{"opus": 1000})
	ctx := context.Background()

	if err := limiter.Record(ctx, "opus", "agent", 0); err != nil {
		t.Fatalf("Record(0) error: %v", err)
	}
	if err := limiter.Record(ctx, "opus", "agent", -5); err != nil {
		t.Fatalf("Record(-5) error: %v", err)
	}

	used, _ := limiter.store.UsageByModel(ctx, "opus")
	if used != 0 {
		t.Errorf("expected 0 tokens stored for zero/negative input, got %d", used)
	}
}

// --- NewStore error paths ---

func TestNewStore_FileAsDirectoryComponent(t *testing.T) {
	f, err := os.CreateTemp("", "not-a-dir-*")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())

	// Use the file as a path component — MkdirAll should fail.
	_, err = NewStore(filepath.Join(f.Name(), "tokens.db"))
	if err == nil {
		t.Fatal("expected error when a path component is a file, not a directory")
	}
}
