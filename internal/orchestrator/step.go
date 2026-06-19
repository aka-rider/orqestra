package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/harness"
)

// Step is a typed pipeline transform: Run takes In, returns Out.
// Each step returns its result and error; no channel signals completion.
// ctx.Err() distinguishes cancel from timeout from error.
type Step[In, Out any] interface {
	ID() AgentID
	Run(ctx context.Context, in In, sc StepContext) (Out, error)
}

// StepContext carries cross-cutting capabilities by value.
// ctx is never stored — it arrives as an argument to each Step.Run call.
type StepContext struct {
	Exec      harness.Executor    // P1: returns a value, never blocks the pipeline
	Obs       Observer            // P4: one-way, lossy observation
	Artifacts ArtifactSink        // P7: fail-closed at integrity boundaries
	Control   Control             // P5: gate request/response + live Post handle
	Sessions  agent.SessionDir    // session artifact directory
	Log       *slog.Logger
	RepoPath  string              // absolute path to the repository root; forwarded to ReadPlan
}

// preferReport returns the agent's output by reading the native plan file.
// allowFallback is forwarded to agent.ReadPlan to enable last-resort JSONL text extraction.
func preferReport(sc StepContext, agentID string, res harness.RunResult, allowFallback bool) (string, bool, error) {
	content, usedFallback, err := agent.ReadPlan(res.SessionID, res.PlanFilePath, sc.RepoPath, allowFallback)
	if err != nil {
		return "", false, fmt.Errorf("read plan: %w", err)
	}
	return content, usedFallback, nil
}

// extractWithFallback extracts the agent output via preferReport. If extraction fails
// or qualityCheck returns an error, it resumes the session via nativeFallback and asks
// the model to write its plan natively. qualityCheck may be nil — in that case only
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
			return nativeFallback(ctx, agentID, spec, res.SessionID, fallbackPrompt, sc)
		}
		return "", false, err
	}
	if qualityCheck != nil {
		if qErr := qualityCheck(report); qErr != nil {
			sc.Log.Warn("report failed quality check, attempting native fallback",
				"agent", agentID, "err", qErr)
			if ctx.Err() == nil && res.SessionID != "" {
				return nativeFallback(ctx, agentID, spec, res.SessionID, fallbackPrompt, sc)
			}
			return "", false, fmt.Errorf("report quality: %w", qErr)
		}
	}
	return report, usedFallback, nil
}

// extractPlan handles the post-exec plan extraction with recovery.
// When runErr is nil or recoverable (context alive, session known), it delegates
// to extractWithFallback, which tries JSONL extraction first and nativeFallback
// second. Otherwise returns runErr unchanged.
func extractPlan(
	ctx context.Context,
	agentID string,
	spec harness.ProcessSpec,
	res harness.RunResult,
	runErr error,
	fallbackPrompt string,
	qualityCheck func(string) error,
	sc StepContext,
) (string, bool, error) {
	if runErr == nil || (ctx.Err() == nil && res.SessionID != "") {
		return extractWithFallback(ctx, agentID, spec, res, fallbackPrompt, qualityCheck, sc)
	}
	return "", false, runErr
}

// nativeFallback resumes an agent session and asks the model to write its plan
// using Claude Code's native plan mechanism. It then extracts the result via ReadPlan.
func nativeFallback(
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
	if spec.Timeout > 0 {
		fbSpec.Timeout = spec.Timeout
	} else {
		fbSpec.Timeout = 3 * time.Minute
	}
	fbSpec.Prompt = fallbackPrompt

	sink := SinkFromObserver(AgentID(agentID), sc.Obs)
	fbRes, runErr := sc.Exec.Run(ctx, fbSpec, nil, sink)

	report, usedFallback, err := preferReport(sc, agentID, fbRes, true)
	if err != nil {
		if runErr != nil {
			return "", false, fmt.Errorf("native fallback exec: %w; plan extraction: %w", runErr, err)
		}
		return "", false, fmt.Errorf("native fallback plan extraction: %w", err)
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
