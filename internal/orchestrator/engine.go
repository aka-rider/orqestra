package orchestrator

import "context"

// Start launches the pipeline in a goroutine and returns immediately. The
// caller observes the run via handle.Events and submits gate decisions /
// question answers via handle.Intents (WP10/RC1 — "one pipe out, one pipe
// in"). The pipeline can never block on the caller.
func (e *Engine) Start(ctx context.Context, input Input) RunHandle {
	return e.startNew(ctx, input)
}
