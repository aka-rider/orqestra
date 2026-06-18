package orchestrator

import (
	"context"

	"github.com/xiii/orqestra/internal/harness"
)

// watchdogExecutor wraps an inner Executor and applies a per-run wall-clock
// timeout when ProcessSpec.Timeout > 0. Zero means no extra deadline.
type watchdogExecutor struct {
	inner harness.Executor
}

// NewWatchdogExecutor returns an Executor that cancels runs exceeding
// spec.Timeout without replacing any context deadline already set by the caller.
func NewWatchdogExecutor(inner harness.Executor) harness.Executor {
	return &watchdogExecutor{inner: inner}
}

func (w *watchdogExecutor) Run(ctx context.Context, spec harness.ProcessSpec, in <-chan harness.Message, sink harness.Sink) (harness.RunResult, error) {
	if spec.Timeout <= 0 {
		return w.inner.Run(ctx, spec, in, sink)
	}
	ctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	return w.inner.Run(ctx, spec, in, sink)
}
