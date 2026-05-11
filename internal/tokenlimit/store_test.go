package tokenlimit

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestStore_RecordAndQuery(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.Record(ctx, "opus", "architect", 1000); err != nil {
		t.Fatalf("Record() error: %v", err)
	}
	if err := store.Record(ctx, "opus", "architect", 500); err != nil {
		t.Fatalf("Record() error: %v", err)
	}
	if err := store.Record(ctx, "opus", "reviewer", 200); err != nil {
		t.Fatalf("Record() error: %v", err)
	}

	total, err := store.UsageByModel(ctx, "opus")
	if err != nil {
		t.Fatalf("UsageByModel() error: %v", err)
	}
	if total != 1700 {
		t.Errorf("UsageByModel() = %d, want 1700", total)
	}

	agents, err := store.UsageByModelAgent(ctx, "opus")
	if err != nil {
		t.Fatalf("UsageByModelAgent() error: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}
	// Ordered by tokens_used DESC
	if agents[0].AgentID != "architect" || agents[0].TokensUsed != 1500 {
		t.Errorf("agents[0] = %+v, want planner/1500", agents[0])
	}
	if agents[1].AgentID != "reviewer" || agents[1].TokensUsed != 200 {
		t.Errorf("agents[1] = %+v, want reviewer/200", agents[1])
	}
}

func TestStore_UsageByModel_Empty(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	total, err := store.UsageByModel(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("UsageByModel() error: %v", err)
	}
	if total != 0 {
		t.Errorf("expected 0 for unused model, got %d", total)
	}
}

func TestStore_AllUsage(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	store.Record(ctx, "opus", "a", 100)
	store.Record(ctx, "opus", "b", 200)
	store.Record(ctx, "sonnet", "a", 50)

	all, err := store.AllUsage(ctx)
	if err != nil {
		t.Fatalf("AllUsage() error: %v", err)
	}
	if all["opus"] != 300 {
		t.Errorf("opus = %d, want 300", all["opus"])
	}
	if all["sonnet"] != 50 {
		t.Errorf("sonnet = %d, want 50", all["sonnet"])
	}
}

func TestStore_ResetModel(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	store.Record(ctx, "opus", "a", 1000)
	store.Record(ctx, "sonnet", "b", 500)

	if err := store.ResetModel(ctx, "opus"); err != nil {
		t.Fatalf("ResetModel() error: %v", err)
	}

	total, _ := store.UsageByModel(ctx, "opus")
	if total != 0 {
		t.Errorf("opus after reset = %d, want 0", total)
	}
	total, _ = store.UsageByModel(ctx, "sonnet")
	if total != 500 {
		t.Errorf("sonnet should be unaffected, got %d", total)
	}
}

func TestStore_ResetAll(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	store.Record(ctx, "opus", "a", 1000)
	store.Record(ctx, "sonnet", "b", 500)

	if err := store.ResetAll(ctx); err != nil {
		t.Fatalf("ResetAll() error: %v", err)
	}

	all, _ := store.AllUsage(ctx)
	if len(all) != 0 {
		t.Errorf("expected empty after reset, got %v", all)
	}
}

func TestStore_ConcurrentWrites(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.Record(ctx, "opus", "agent", 100)
		}()
	}
	wg.Wait()

	total, err := store.UsageByModel(ctx, "opus")
	if err != nil {
		t.Fatalf("UsageByModel() error: %v", err)
	}
	if total != 5000 {
		t.Errorf("concurrent total = %d, want 5000", total)
	}
}

func TestLimiter_CheckUnderBudget(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	limits := map[string]int64{"opus": 1000}
	lim := NewLimiter(store, limits)

	store.Record(ctx, "opus", "agent", 500)

	if err := lim.Check(ctx, "opus", "agent"); err != nil {
		t.Fatalf("Check() should pass under budget: %v", err)
	}
}

func TestLimiter_CheckOverBudget(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	limits := map[string]int64{"opus": 1000}
	lim := NewLimiter(store, limits)

	store.Record(ctx, "opus", "agent", 1000)

	err := lim.Check(ctx, "opus", "agent")
	if err == nil {
		t.Fatal("Check() should fail when at budget")
	}
	if !IsBudgetExhausted(err) {
		t.Fatalf("expected ErrBudgetExhausted, got: %v", err)
	}
	var budgetErr *ErrBudgetExhausted
	if ok := errors.As(err, &budgetErr); !ok {
		t.Fatal("should unwrap to ErrBudgetExhausted")
	}
	if budgetErr.Model != "opus" {
		t.Errorf("Model = %q, want opus", budgetErr.Model)
	}
	if budgetErr.Used != 1000 {
		t.Errorf("Used = %d, want 1000", budgetErr.Used)
	}
}

func TestLimiter_CheckNoLimit(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	limits := map[string]int64{"opus": 1000}
	lim := NewLimiter(store, limits)

	// Model not in limits map — should always pass
	store.Record(ctx, "sonnet", "agent", 999999)
	if err := lim.Check(ctx, "sonnet", "agent"); err != nil {
		t.Fatalf("Check() should pass for unconfigured model: %v", err)
	}
}

func TestLimiter_RecordPushesOverBudget(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	limits := map[string]int64{"opus": 1000}
	lim := NewLimiter(store, limits)

	store.Record(ctx, "opus", "agent", 800)

	err := lim.Record(ctx, "opus", "agent", 300)
	if err == nil {
		t.Fatal("Record() should return error when pushing over budget")
	}
	if !IsBudgetExhausted(err) {
		t.Fatalf("expected ErrBudgetExhausted, got: %v", err)
	}
}

func TestLimiter_RecordUnderBudget(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	limits := map[string]int64{"opus": 1000}
	lim := NewLimiter(store, limits)

	if err := lim.Record(ctx, "opus", "agent", 500); err != nil {
		t.Fatalf("Record() should pass when under budget: %v", err)
	}
}

func TestLimiter_Status(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	limits := map[string]int64{"opus": 1000}
	lim := NewLimiter(store, limits)

	store.Record(ctx, "opus", "architect", 300)
	store.Record(ctx, "opus", "worker", 200)

	status, err := lim.Status(ctx, "opus")
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}
	if status.Limit != 1000 {
		t.Errorf("Limit = %d, want 1000", status.Limit)
	}
	if status.Used != 500 {
		t.Errorf("Used = %d, want 500", status.Used)
	}
	if status.Remaining != 500 {
		t.Errorf("Remaining = %d, want 500", status.Remaining)
	}
	if len(status.ByAgent) != 2 {
		t.Errorf("ByAgent len = %d, want 2", len(status.ByAgent))
	}
}

func TestLimiter_HasLimit(t *testing.T) {
	store := newTestStore(t)
	limits := map[string]int64{"opus": 1000}
	lim := NewLimiter(store, limits)

	if !lim.HasLimit("opus") {
		t.Error("HasLimit(opus) should be true")
	}
	if lim.HasLimit("sonnet") {
		t.Error("HasLimit(sonnet) should be false")
	}
}
