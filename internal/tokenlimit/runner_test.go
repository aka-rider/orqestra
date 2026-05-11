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
	limiter := newTestLimiter(t, map[string]int64{"large": 100})
	// Pre-exhaust the budget.
	limiter.store.Record(context.Background(), "large", "other", 200)

	runner := NewLimitedRunner(inner, limiter, "large", "architect")
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
	limiter := newTestLimiter(t, map[string]int64{"large": 10000})
	runner := NewLimitedRunner(inner, limiter, "large", "architect")

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
				Usage:  harness.TokenUsage{TotalTokens: 900},
			}, nil
		},
	}
	// Limit 1000, already used 500 — adding 900 will exceed.
	limiter := newTestLimiter(t, map[string]int64{"large": 1000})
	limiter.store.Record(context.Background(), "large", "prior", 500)

	runner := NewLimitedRunner(inner, limiter, "large", "architect")
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
				Usage: harness.TokenUsage{TotalTokens: 0},
			}, nil
		},
	}
	limiter := newTestLimiter(t, map[string]int64{"large": 1000})
	runner := NewLimitedRunner(inner, limiter, "large", "architect")

	_, err := runner.RunPrint(context.Background(), "prompt", "sys")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	used, _ := limiter.store.UsageByModel(context.Background(), "large")
	if used != 0 {
		t.Errorf("expected 0 tokens recorded, got %d", used)
	}
}

func TestLimitedRunner_RunPrint_RecordsOnInnerError(t *testing.T) {
	innerErr := errors.New("execution failed")
	inner := &mockCLIRunner{
		runPrintFn: func(_ context.Context, _, _ string) (harness.RunResult, error) {
			return harness.RunResult{
				Usage: harness.TokenUsage{TotalTokens: 300},
			}, innerErr
		},
	}
	limiter := newTestLimiter(t, map[string]int64{"large": 10000})
	runner := NewLimitedRunner(inner, limiter, "large", "architect")

	_, err := runner.RunPrint(context.Background(), "prompt", "sys")
	if !errors.Is(err, innerErr) {
		t.Fatalf("expected innerErr, got %v", err)
	}
	// Tokens must be recorded even though inner errored.
	used, _ := limiter.store.UsageByModel(context.Background(), "large")
	if used != 300 {
		t.Errorf("expected 300 tokens recorded, got %d", used)
	}
}

func TestLimitedRunner_RunPrint_NilUsageSkipsRecord(t *testing.T) {
	inner := &mockCLIRunner{
		runPrintFn: func(_ context.Context, _, _ string) (harness.RunResult, error) {
			return harness.RunResult{}, nil
		},
	}
	limiter := newTestLimiter(t, map[string]int64{"large": 1000})
	runner := NewLimitedRunner(inner, limiter, "large", "architect")

	_, err := runner.RunPrint(context.Background(), "prompt", "sys")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	used, _ := limiter.store.UsageByModel(context.Background(), "large")
	if used != 0 {
		t.Errorf("expected 0 tokens, got %d", used)
	}
}

func TestLimitedRunner_RunPrint_NoLimit_AlwaysPasses(t *testing.T) {
	inner := &mockCLIRunner{
		runPrintFn: func(_ context.Context, _, _ string) (harness.RunResult, error) {
			return harness.RunResult{
				Output: "ok",
				Usage:  harness.TokenUsage{TotalTokens: 999999},
			}, nil
		},
	}
	// No limit configured for "large".
	limiter := newTestLimiter(t, map[string]int64{})
	runner := NewLimitedRunner(inner, limiter, "large", "architect")

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
	limiter := newTestLimiter(t, map[string]int64{"large": 100})
	limiter.store.Record(context.Background(), "large", "other", 200)

	runner := NewLimitedRunner(inner, limiter, "large", "worker")
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
				Usage: harness.TokenUsage{TotalTokens: 150},
			}, innerErr
		},
	}
	limiter := newTestLimiter(t, map[string]int64{"large": 10000})
	runner := NewLimitedRunner(inner, limiter, "large", "worker")

	_, err := runner.RunStreaming(context.Background(), "prompt", "sys", io.Discard)
	if !errors.Is(err, innerErr) {
		t.Fatalf("expected innerErr, got %v", err)
	}
	used, _ := limiter.store.UsageByModel(context.Background(), "large")
	if used != 150 {
		t.Errorf("expected 150 tokens recorded, got %d", used)
	}
}

func TestLimitedRunner_RunStreaming_PostRecordBudgetError(t *testing.T) {
	inner := &mockCLIRunner{
		runStreamingFn: func(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
			return harness.RunResult{
				Output: "streamed",
				Usage:  harness.TokenUsage{TotalTokens: 600},
			}, nil
		},
	}
	limiter := newTestLimiter(t, map[string]int64{"large": 1000})
	limiter.store.Record(context.Background(), "large", "prior", 500)

	runner := NewLimitedRunner(inner, limiter, "large", "worker")
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
		AgentID: "architect",
		Used:    1500,
		Limit:   1000,
	}
	msg := err.Error()
	for _, want := range []string{"claude-opus-4-5", "architect", "1500", "1000"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q missing %q", msg, want)
		}
	}
}

// --- Limiter.StatusAll ---

func TestLimiter_StatusAll(t *testing.T) {
	limiter := newTestLimiter(t, map[string]int64{
		"large":   10000,
		"sonnet": 5000,
	})
	ctx := context.Background()
	limiter.store.Record(ctx, "large", "architect", 2000)
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

	opus := byModel["large"]
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
	limiter := newTestLimiter(t, map[string]int64{"large": 1000})
	ctx := context.Background()

	if err := limiter.Record(ctx, "large", "agent", 0); err != nil {
		t.Fatalf("Record(0) error: %v", err)
	}
	if err := limiter.Record(ctx, "large", "agent", -5); err != nil {
		t.Fatalf("Record(-5) error: %v", err)
	}

	used, _ := limiter.store.UsageByModel(ctx, "large")
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

// --- LimitedRunner.RunStreaming passthrough ---

func TestLimitedRunner_RunStreaming_PassthroughOnBudgetOK(t *testing.T) {
	inner := &mockCLIRunner{
		runStreamingFn: func(_ context.Context, _, _ string, stdout io.Writer) (harness.RunResult, error) {
			stdout.Write([]byte("streamed text"))
			return harness.RunResult{Output: "streamed text"}, nil
		},
	}
	limiter := newTestLimiter(t, map[string]int64{"large": 10000})
	runner := NewLimitedRunner(inner, limiter, "large", "worker")

	var buf strings.Builder
	result, err := runner.RunStreaming(context.Background(), "prompt", "sys", &buf)
	if err != nil {
		t.Fatalf("RunStreaming() error: %v", err)
	}
	if result.Output != "streamed text" {
		t.Errorf("output = %q, want %q", result.Output, "streamed text")
	}
	if buf.String() != "streamed text" {
		t.Errorf("writer = %q, want %q", buf.String(), "streamed text")
	}
}

// --- LimitedRunner.RunStreaming nil usage ---

func TestLimitedRunner_RunStreaming_NilUsageSkipsRecord(t *testing.T) {
	inner := &mockCLIRunner{
		runStreamingFn: func(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
			return harness.RunResult{}, nil
		},
	}
	limiter := newTestLimiter(t, map[string]int64{"large": 1000})
	runner := NewLimitedRunner(inner, limiter, "large", "worker")

	_, err := runner.RunStreaming(context.Background(), "prompt", "sys", io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	used, _ := limiter.store.UsageByModel(context.Background(), "large")
	if used != 0 {
		t.Errorf("expected 0 tokens, got %d", used)
	}
}

// --- LimitedRunner.RunStreaming zero tokens ---

func TestLimitedRunner_RunStreaming_ZeroTokensSkipsRecord(t *testing.T) {
	inner := &mockCLIRunner{
		runStreamingFn: func(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
			return harness.RunResult{
				Usage: harness.TokenUsage{TotalTokens: 0},
			}, nil
		},
	}
	limiter := newTestLimiter(t, map[string]int64{"large": 1000})
	runner := NewLimitedRunner(inner, limiter, "large", "worker")

	_, err := runner.RunStreaming(context.Background(), "prompt", "sys", io.Discard)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	used, _ := limiter.store.UsageByModel(context.Background(), "large")
	if used != 0 {
		t.Errorf("expected 0 tokens recorded, got %d", used)
	}
}

// --- LimitedRunner.RunStreaming no limit ---

func TestLimitedRunner_RunStreaming_NoLimit_AlwaysPasses(t *testing.T) {
	inner := &mockCLIRunner{
		runStreamingFn: func(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
			return harness.RunResult{
				Output: "ok",
				Usage:  harness.TokenUsage{TotalTokens: 999999},
			}, nil
		},
	}
	limiter := newTestLimiter(t, map[string]int64{})
	runner := NewLimitedRunner(inner, limiter, "large", "worker")

	result, err := runner.RunStreaming(context.Background(), "prompt", "sys", io.Discard)
	if err != nil {
		t.Fatalf("expected no error with unlimited model, got %v", err)
	}
	if result.Output != "ok" {
		t.Errorf("output = %q, want %q", result.Output, "ok")
	}
}

// --- Limiter.Status error from UsageByModelAgent ---

func TestLimiter_Status_ByAgentBreakdown(t *testing.T) {
	limiter := newTestLimiter(t, map[string]int64{"large": 5000})
	ctx := context.Background()
	limiter.store.Record(ctx, "large", "architect", 1000)
	limiter.store.Record(ctx, "large", "worker", 500)

	status, err := limiter.Status(ctx, "large")
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}
	if status.Used != 1500 {
		t.Errorf("used = %d, want 1500", status.Used)
	}
	if status.Remaining != 3500 {
		t.Errorf("remaining = %d, want 3500", status.Remaining)
	}
	if len(status.ByAgent) != 2 {
		t.Errorf("byAgent count = %d, want 2", len(status.ByAgent))
	}
}

func TestLimiter_Status_Unlimited(t *testing.T) {
	limiter := newTestLimiter(t, map[string]int64{}) // no limit for "large"
	ctx := context.Background()
	limiter.store.Record(ctx, "large", "architect", 1000)

	status, err := limiter.Status(ctx, "large")
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}
	if status.Remaining != -1 {
		t.Errorf("remaining = %d, want -1 (unlimited)", status.Remaining)
	}
}
