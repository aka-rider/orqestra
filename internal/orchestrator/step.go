package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

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

// extractWithFallback extracts the agent report via preferReport. If extraction fails
// or qualityCheck returns an error, it resumes the session via mcpFallback and asks
// the model to call SubmitReport. qualityCheck may be nil — in that case only
// extraction failure triggers the fallback.
func extractWithFallback(
	ctx context.Context,
	agentID string,
	spec harness.ProcessSpec,
	res harness.RunResult,
	fallbackPrompt string,
	qualityCheck func(string) error,
	sc StepContext,
) (string, bool, error) {
	report, usedFallback, err := preferReport(sc, agentID, res, true)
	if err != nil {
		if ctx.Err() == nil && res.SessionID != "" {
			return mcpFallback(ctx, agentID, spec, res.SessionID, fallbackPrompt, sc)
		}
		return "", false, err
	}
	if qualityCheck != nil {
		if qErr := qualityCheck(report); qErr != nil {
			sc.Log.Warn("report failed quality check, attempting MCP fallback",
				"agent", agentID, "err", qErr)
			if ctx.Err() == nil && res.SessionID != "" {
				return mcpFallback(ctx, agentID, spec, res.SessionID, fallbackPrompt, sc)
			}
			return "", false, fmt.Errorf("report quality: %w", qErr)
		}
	}
	return report, usedFallback, nil
}

// mcpFallback resumes an agent session and requests a SubmitReport call.
// If SubmitReport is not called, it falls back to plan-file extraction from
// the resumed session. The report is returned regardless of quality — callers
// that need quality assurance should call checkX before or after.
func mcpFallback(
	ctx context.Context,
	agentID string,
	spec harness.ProcessSpec,
	sessionID string,
	fallbackPrompt string,
	sc StepContext,
) (string, bool, error) {
	fbSpec := spec
	fbSpec.Resume = harness.ResumeSession(sessionID)
	fbSpec.SteerOnLoop = false
	fbSpec.PreTimeoutNudge = ""
	fbSpec.LoopGuard = harness.LoopGuardSpec{}
	fbSpec.Timeout = 3 * time.Minute
	fbSpec.Prompt = fallbackPrompt

	sink := SinkFromObserver(AgentID(agentID), sc.Obs)
	fbRes, runErr := sc.Exec.Run(ctx, fbSpec, nil, sink)

	if sc.Reports != nil {
		if r, ok := sc.Reports.TakeReport(agentID); ok {
			_ = runErr // fire-and-forget: SubmitReport was delivered before session end; exec error is moot
			return r.Report, false, nil
		}
	}

	report, usedFallback, err := preferReport(sc, agentID, fbRes, true)
	if err != nil {
		if runErr != nil {
			return "", false, fmt.Errorf("mcp fallback exec: %w; plan extraction: %w", runErr, err)
		}
		return "", false, fmt.Errorf("mcp fallback plan extraction: %w", err)
	}
	return report, usedFallback, nil
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
