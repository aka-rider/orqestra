package orchestrator

import (
	"context"

	"github.com/xiii/orqestra/internal/mcp"
)

// Start launches the pipeline in a goroutine and returns immediately.
// The caller observes state via handle.Obs.Snapshot() and submits decisions
// via handle.Ctrl.Submit(). The pipeline can never block on the caller.
func (e *Engine) Start(ctx context.Context, input Input) RunHandle {
	return e.startNew(ctx, input)
}

// Run executes the pipeline synchronously and blocks until it finishes.
func (e *Engine) Run(ctx context.Context, input Input) (Result, error) {
	handle := e.Start(ctx, input)
	for {
		snap := handle.Obs.Snapshot()
		if snap.Terminal.Done {
			return snap.Terminal.Result, snap.Terminal.Err
		}
		select {
		case <-handle.Obs.NotifyCh():
		case <-ctx.Done():
			return Result{Status: StatusFailed}, ctx.Err()
		}
	}
}

// SendAnswer delivers the user's answer to the waiting MCP bridge subprocess.
// No-op when QuestionBridge is nil.
func (e *Engine) SendAnswer(ans mcp.Answer) {
	if e.QuestionBridge == nil {
		return
	}
	e.QuestionBridge.SendAnswer(ans)
}
