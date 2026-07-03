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

// SendAnswer delivers the user's answer to the waiting MCP bridge subprocess.
// No-op when QuestionBridge is nil.
func (e *Engine) SendAnswer(ans mcp.Answer) {
	if e.QuestionBridge == nil {
		return
	}
	e.QuestionBridge.SendAnswer(ans)
}
