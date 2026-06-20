package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
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

// qualityPassOrTrim returns (report, true) if report passes qualityCheck as-is, or
// (trimmed, true) with the text trimmed to start at the first '#'-prefixed line that
// makes qualityCheck pass. Returns ("", false) if no such position exists.
func qualityPassOrTrim(report string, qualityCheck func(string) error) (string, bool) {
	if qualityCheck(report) == nil {
		return report, true
	}
	lines := strings.Split(report, "\n")
	for i := 1; i < len(lines); i++ {
		if !strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
			continue
		}
		candidate := strings.TrimSpace(strings.Join(lines[i:], "\n"))
		if candidate != "" && qualityCheck(candidate) == nil {
			return candidate, true
		}
	}
	return "", false
}

// runFallbackWithQuality runs nativeFallback and applies qualityCheck (with preamble
// trimming) to its result. Returns error if the result still fails quality.
func runFallbackWithQuality(
	ctx context.Context,
	agentID string,
	spec harness.ProcessSpec,
	sessionID string,
	fallbackPrompt string,
	qualityCheck func(string) error,
	sc StepContext,
) (string, bool, error) {
	fbReport, fbUsed, fbErr := nativeFallback(ctx, agentID, spec, sessionID, fallbackPrompt, sc)
	if fbErr != nil {
		return "", false, fbErr
	}
	if qualityCheck == nil {
		return fbReport, fbUsed, nil
	}
	if out, ok := qualityPassOrTrim(fbReport, qualityCheck); ok {
		return out, fbUsed, nil
	}
	return "", false, fmt.Errorf("native fallback: model output does not meet format requirements after recovery")
}

// extractWithFallback extracts the agent output via preferReport. If extraction fails
// or qualityCheck returns an error, it resumes the session via runFallbackWithQuality
// which applies quality check (with preamble trimming) to the fallback result too.
// qualityCheck may be nil — in that case only extraction failure triggers the fallback.
func extractWithFallback(
	ctx context.Context,
	agentID string,
	spec harness.ProcessSpec,
	res harness.RunResult,
	fallbackPrompt string,
	qualityCheck func(string) error,
	sc StepContext,
) (string, bool, error) {
	canFallback := ctx.Err() == nil && res.SessionID != ""

	report, usedFallback, err := preferReport(sc, agentID, res, true)
	if err != nil {
		if canFallback {
			return runFallbackWithQuality(ctx, agentID, spec, res.SessionID, fallbackPrompt, qualityCheck, sc)
		}
		return "", false, err
	}

	if qualityCheck != nil {
		if out, ok := qualityPassOrTrim(report, qualityCheck); ok {
			return out, usedFallback, nil
		}
		sc.Log.Warn("report failed quality check, attempting native fallback",
			"agent", agentID)
		if canFallback {
			return runFallbackWithQuality(ctx, agentID, spec, res.SessionID, fallbackPrompt, qualityCheck, sc)
		}
		return "", false, fmt.Errorf("report quality: model output does not meet format requirements")
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

// finalMessage returns an agent's final assistant text for roles that run OUTSIDE
// plan mode (researcher, critic) and therefore never write a plan file. It prefers
// the result event's output and falls back to the session JSONL's final message.
// It deliberately avoids agent.ReadPlan's plan-file tiers — notably the
// scan-~/.claude/plans fallback, which could return a stale plan from a different
// run when no plan file exists.
func finalMessage(sc StepContext, res harness.RunResult) (string, error) {
	if out := strings.TrimSpace(res.Output); out != "" {
		return out, nil
	}
	if res.SessionID == "" {
		return "", fmt.Errorf("no session output and no session ID")
	}
	jsonl, err := harness.ResolveSessionLogPath(sc.RepoPath, res.SessionID)
	if err != nil {
		return "", fmt.Errorf("resolve session log for %s: %w", res.SessionID, err)
	}
	out, err := harness.ExtractFinalOutput(jsonl)
	if err != nil {
		return "", fmt.Errorf("extract final message for %s: %w", res.SessionID, err)
	}
	if out = strings.TrimSpace(out); out == "" {
		return "", fmt.Errorf("session %s produced no final message", res.SessionID)
	}
	return out, nil
}

// extractFinalMessage harvests an off-plan-mode agent's report from its final
// assistant message and applies qualityCheck. On extraction or quality failure it
// resumes the session once with fallbackPrompt and re-harvests. Mirrors
// extractWithFallback but for roles that do not use plan files.
func extractFinalMessage(
	ctx context.Context,
	agentID string,
	spec harness.ProcessSpec,
	res harness.RunResult,
	runErr error,
	fallbackPrompt string,
	qualityCheck func(string) error,
	sc StepContext,
) (string, error) {
	canRecover := ctx.Err() == nil && res.SessionID != ""
	if runErr != nil && !canRecover {
		return "", runErr
	}

	report, err := finalMessage(sc, res)
	if err != nil {
		if canRecover {
			return finalMessageFallback(ctx, agentID, spec, res.SessionID, fallbackPrompt, qualityCheck, sc)
		}
		return "", err
	}

	if qualityCheck != nil {
		if out, ok := qualityPassOrTrim(report, qualityCheck); ok {
			return out, nil
		}
		sc.Log.Warn("report failed quality check, attempting recovery", "agent", agentID)
		if canRecover {
			return finalMessageFallback(ctx, agentID, spec, res.SessionID, fallbackPrompt, qualityCheck, sc)
		}
		return "", fmt.Errorf("%s report quality: output does not meet role requirements", agentID)
	}
	return report, nil
}

// finalMessageFallback resumes a session with fallbackPrompt and re-harvests the
// final message, applying qualityCheck (with preamble trimming) to the result.
func finalMessageFallback(
	ctx context.Context,
	agentID string,
	spec harness.ProcessSpec,
	sessionID string,
	fallbackPrompt string,
	qualityCheck func(string) error,
	sc StepContext,
) (string, error) {
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

	report, err := finalMessage(sc, fbRes)
	if err != nil {
		if runErr != nil {
			return "", fmt.Errorf("%s recovery exec: %w; harvest: %w", agentID, runErr, err)
		}
		return "", fmt.Errorf("%s recovery harvest: %w", agentID, err)
	}
	if qualityCheck == nil {
		return report, nil
	}
	if out, ok := qualityPassOrTrim(report, qualityCheck); ok {
		return out, nil
	}
	return "", fmt.Errorf("%s output does not meet role requirements after recovery", agentID)
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
