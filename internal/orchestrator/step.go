package orchestrator

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/mcp"
)

// Step is a typed pipeline transform: Run takes In, returns Out.
// Each step returns its result and error; no channel signals completion.
// ctx.Err() distinguishes cancel from timeout from error.
type Step[In, Out any] interface {
	ID() AgentID
	Run(ctx context.Context, in In, sc StepContext) (Out, error)
}

// ReportTaker retrieves and removes a submitted report for the given agent role.
// Returns false when the agent did not call SubmitReport during its run.
type ReportTaker interface {
	TakeReport(agentID string) (mcp.ReportSubmission, bool)
}

// StepContext carries cross-cutting capabilities by value.
// ctx is never stored — it arrives as an argument to each Step.Run call.
type StepContext struct {
	Exec      harness.Executor    // P1: returns a value, never blocks the pipeline
	Obs       Observer            // P4: one-way, lossy observation
	Artifacts ArtifactSink        // P7: fail-closed at integrity boundaries
	Control   Control             // P5: gate request/response + live Post handle
	Sessions  agent.SessionDir    // session artifact directory
	Reports   ReportTaker         // optional: nil means no MCP bridge
	Log       *slog.Logger
	RepoPath  string              // absolute path to the repository root; forwarded to ReadPlan
}

// preferReport returns the agent's output, preferring a SubmitReport inbox entry
// over the plan file. When the bridge has a report, usedFallback is always false.
// allowFallback is forwarded to agent.ReadPlan when no report was submitted.
func preferReport(sc StepContext, agentID string, res harness.RunResult, allowFallback bool) (string, bool, error) {
	if sc.Reports != nil {
		if r, ok := sc.Reports.TakeReport(agentID); ok {
			if r.Summary != "" {
				sc.Log.Info("agent report", "agent", agentID, "summary", r.Summary)
			}
			return r.Report, false, nil
		}
	}
	content, usedFallback, err := agent.ReadPlan(res.SessionID, res.PlanFilePath, sc.RepoPath, allowFallback)
	if err != nil {
		return "", false, fmt.Errorf("read plan: %w", err)
	}
	return content, usedFallback, nil
}

// SinkFromObserver returns a harness.Sink that forwards all events to Observer.Stream.
// Used by step implementations to bridge harness.Executor output to the observer bus.
func SinkFromObserver(id AgentID, obs Observer) harness.Sink {
	return &observerSink{id: id, obs: obs}
}

// observerSink implements harness.Sink by forwarding to Observer.Stream.
// Observe must not block — Observer.Stream is non-blocking by contract.
type observerSink struct {
	id  AgentID
	obs Observer
}

func (s *observerSink) Observe(ev harness.Event) { s.obs.Stream(s.id, ev) }
