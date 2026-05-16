package tokenlimit

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// ErrBudgetExhausted is returned when a model's token budget is exceeded.
type ErrBudgetExhausted struct {
	Model   string
	AgentID string
	Used    int64
	Limit   int64
}

func (e *ErrBudgetExhausted) Error() string {
	return fmt.Sprintf("token budget exhausted for model %q: %d / %d tokens (triggered by agent %q)",
		e.Model, e.Used, e.Limit, e.AgentID)
}

// IsBudgetExhausted returns true if err is or wraps an ErrBudgetExhausted.
func IsBudgetExhausted(err error) bool {
	var target *ErrBudgetExhausted
	return errors.As(err, &target)
}

// ModelStatus represents the current token usage state for a model.
type ModelStatus struct {
	Model     string
	Limit     int64
	Used      int64
	Remaining int64
	ByAgent   []AgentUsage
}

// Limiter enforces per-model token budgets backed by persistent storage.
type Limiter struct {
	store  *Store
	limits map[string]int64 // underlying model string → budget
}

// NewLimiter creates a limiter with the given store and model budgets.
func NewLimiter(store *Store, limits map[string]int64) *Limiter {
	return &Limiter{
		store:  store,
		limits: limits,
	}
}

// Check verifies that a model is still within budget before making a call.
// Returns ErrBudgetExhausted if the budget is already exceeded.
// Returns nil if the model has no configured limit.
func (l *Limiter) Check(ctx context.Context, model, agentID string) error {
	limit, ok := l.limits[model]
	if !ok || limit <= 0 {
		return nil // no limit configured
	}

	used, err := l.store.UsageByModel(ctx, model)
	if err != nil {
		return err
	}

	if used >= limit {
		return &ErrBudgetExhausted{
			Model:   model,
			AgentID: agentID,
			Used:    used,
			Limit:   limit,
		}
	}
	return nil
}

// Record persists token usage and returns ErrBudgetExhausted if the new total exceeds the limit.
func (l *Limiter) Record(ctx context.Context, model, agentID string, tokens int64) error {
	if tokens <= 0 {
		return nil
	}

	if err := l.store.Record(ctx, model, agentID, tokens); err != nil {
		return err
	}

	limit, ok := l.limits[model]
	if !ok || limit <= 0 {
		return nil
	}

	used, err := l.store.UsageByModel(ctx, model)
	if err != nil {
		return err
	}

	if used > limit {
		return &ErrBudgetExhausted{
			Model:   model,
			AgentID: agentID,
			Used:    used,
			Limit:   limit,
		}
	}
	return nil
}

// Status returns current usage state for a model.
func (l *Limiter) Status(ctx context.Context, model string) (*ModelStatus, error) {
	limit := l.limits[model] // 0 if not configured

	used, err := l.store.UsageByModel(ctx, model)
	if err != nil {
		return nil, err
	}

	byAgent, err := l.store.UsageByModelAgent(ctx, model)
	if err != nil {
		return nil, err
	}

	remaining := limit - used
	if limit <= 0 {
		remaining = -1 // unlimited
	}

	return &ModelStatus{
		Model:     model,
		Limit:     limit,
		Used:      used,
		Remaining: remaining,
		ByAgent:   byAgent,
	}, nil
}

// StatusAll returns usage state for all models that have limits configured.
func (l *Limiter) StatusAll(ctx context.Context) ([]ModelStatus, error) {
	var models []string
	for model := range l.limits {
		models = append(models, model)
	}
	sort.Strings(models)

	var results []ModelStatus
	for _, model := range models {
		status, err := l.Status(ctx, model)
		if err != nil {
			return nil, err
		}
		results = append(results, *status)
	}
	return results, nil
}

// HasLimit returns true if the given model has a configured token budget.
func (l *Limiter) HasLimit(model string) bool {
	limit, ok := l.limits[model]
	return ok && limit > 0
}
