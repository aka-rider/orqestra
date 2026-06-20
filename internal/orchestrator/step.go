package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

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

// ReportStore retrieves agent report submissions delivered via SubmitReport.
// TakeReport returns the report text and true if a submission exists, then
// removes it so it is consumed exactly once.
type ReportStore interface {
	TakeReport(agentID string) (string, bool)
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
	RepoPath  string              // absolute path to the repository root
	Reports   ReportStore         // tier-1 report channel; nil when no bridge is configured
}

// preferReport returns the plan written by the architect to its plan file.
// Uses ReadPlanFile (stream path → JSONL attachment), never the dir-scan fallback.
// Returns an error when no plan file exists for this session.
func preferReport(sc StepContext, agentID string, res harness.RunResult) (string, error) {
	content, err := agent.ReadPlanFile(res.SessionID, res.PlanFilePath, sc.RepoPath)
	if err != nil {
		return "", fmt.Errorf("read plan file for %s: %w", agentID, err)
	}
	return content, nil
}

// finalMessage returns an agent's final assistant text from the run result output
// or the session JSONL. Used as the conversation-probe tier in extractReport.
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

// extractReport is the unified post-run report harvester for all pipeline roles.
// Nudging now lives in the AgentSupervisor; this function only harvests what
// arrived.
//
//	Tier 1: SubmitReport submission   (sc.Reports.TakeReport — if Reports != nil)
//	Tier 2: Plan file                 (preferReport — only when spec.PlanMode is true)
//	Tier 3: Conversation probe        (finalMessage — with looksLikeReport gate)
//	Tier 4: Fail closed               (descriptive error; never bare context.Canceled)
func extractReport(
	_ context.Context,
	spec harness.ProcessSpec,
	res harness.RunResult,
	runErr error,
	sc StepContext,
) (string, error) {
	agentID := spec.AgentID

	// Tier 1: SubmitReport submission.
	if sc.Reports != nil {
		if sub, ok := sc.Reports.TakeReport(agentID); ok && sub != "" {
			if looksLikeReport(sub) {
				return sub, nil
			}
			sc.Log.Warn("submitted report failed sanity check, trying next tier", "agent", agentID)
		}
	}

	// Tier 2: Plan file (architect plan-mode only).
	if spec.PlanMode {
		if plan, err := preferReport(sc, agentID, res); err == nil && looksLikeReport(plan) {
			sc.Log.Warn("report satisfied by plan file (tier 2)", "agent", agentID)
			return plan, nil
		}
	}

	// Tier 3: Conversation probe.
	if msg, err := finalMessage(sc, res); err == nil && looksLikeReport(msg) {
		sc.Log.Warn("report satisfied by conversation probe (tier 3)", "agent", agentID)
		return msg, nil
	}

	// Tier 4: Fail closed.
	if runErr != nil {
		return "", fmt.Errorf("%s failed: %w", agentID, runErr)
	}
	return "", fmt.Errorf("%s: no valid report produced", agentID)
}

// runReportAgent runs spec via sc.Exec (with retries on harness error) and then
// harvests the report via extractReport. It is the DRY entry point for
// researcher, architect, and critic — roles that always produce a structured report.
//
// The caller must call sc.Obs.AgentStarted before invoking; runReportAgent
// re-calls it only on retries (attempt > 1).
func runReportAgent(
	ctx context.Context,
	sc StepContext,
	spec harness.ProcessSpec,
	meta AgentMeta,
	attempts int,
) (harness.RunResult, string, error) {
	if attempts < 1 {
		attempts = 1
	}
	var res harness.RunResult
	var runErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		sink := SinkFromObserver(AgentID(spec.AgentID), sc.Obs)
		res, runErr = sc.Exec.Run(ctx, spec, nil, sink)
		if runErr == nil || attempt >= attempts {
			break
		}
		sc.Log.Warn("agent attempt failed, retrying",
			"agent", spec.AgentID, "attempt", attempt, "err", runErr)
		sc.Obs.AgentStarted(AgentID(spec.AgentID), meta)
	}

	report, err := extractReport(ctx, spec, res, runErr, sc)
	return res, report, err
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
