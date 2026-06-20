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

// finalMessage returns an agent's final assistant text from the run result output
// or the session JSONL. Used as the conversation-probe tier (tier 3) in extractReport.
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

// resumeForReport resumes an agent session with a nudge prompt requesting SubmitReport.
// It zeroes per-run state (LoopGuard, SilenceGuard, PreTimeoutNudge) and defaults
// Timeout to 3 minutes when the original spec has no timeout set.
func resumeForReport(
	ctx context.Context,
	agentID string,
	spec harness.ProcessSpec,
	sessionID string,
	nudgePrompt string,
	sc StepContext,
) (harness.RunResult, error) {
	fbSpec := spec
	fbSpec.Resume = harness.ResumeSession(sessionID)
	fbSpec.PreTimeoutNudge = ""
	fbSpec.LoopGuard = harness.LoopGuardSpec{}
	fbSpec.SilenceGuard = harness.SilenceGuardSpec{}
	if spec.Timeout <= 0 {
		fbSpec.Timeout = 3 * time.Minute
	}
	fbSpec.Prompt = nudgePrompt
	sink := SinkFromObserver(AgentID(agentID), sc.Obs)
	return sc.Exec.Run(ctx, fbSpec, nil, sink)
}

const maxReportNudges = 2

// extractReport is the unified report extraction ladder for all pipeline roles.
// It tries five tiers in order and returns the first candidate that passes
// qualityPassOrTrim. A Warn is logged whenever a tier beyond the first satisfies.
//
//	Tier 1: SubmitReport submission  (sc.Reports.TakeReport — if Reports != nil)
//	Tier 2: Plan file                (preferReport — only when usesPlanFile=true)
//	Tier 3: Conversation probe       (finalMessage — always, including on timeout)
//	Tier 4: Nudge loop               (up to maxReportNudges resumes — only if ctx live)
//	Tier 5: Fail closed              (wraps runErr when present)
func extractReport(
	ctx context.Context,
	agentID string,
	spec harness.ProcessSpec,
	res harness.RunResult,
	runErr error,
	nudgePrompt string,
	qualityCheck func(string) error,
	usesPlanFile bool,
	sc StepContext,
) (string, error) {
	// Tier 1: SubmitReport submission.
	if sc.Reports != nil {
		if sub, ok := sc.Reports.TakeReport(agentID); ok && sub != "" {
			if out, pass := qualityPassOrTrim(sub, qualityCheck); pass {
				return out, nil
			}
			sc.Log.Warn("submitted report failed quality check, trying next tier", "agent", agentID)
		}
	}

	// Tier 2: Plan file (architect only).
	if usesPlanFile {
		if plan, err := preferReport(sc, agentID, res); err == nil {
			if out, pass := qualityPassOrTrim(plan, qualityCheck); pass {
				sc.Log.Warn("report satisfied by plan file (tier 2)", "agent", agentID)
				return out, nil
			}
			sc.Log.Warn("plan file failed quality check, trying conversation probe", "agent", agentID)
		}
	}

	// Tier 3: Conversation probe — reachable even on timeout.
	if msg, err := finalMessage(sc, res); err == nil {
		if out, pass := qualityPassOrTrim(msg, qualityCheck); pass {
			sc.Log.Warn("report satisfied by conversation probe (tier 3)", "agent", agentID)
			return out, nil
		}
		sc.Log.Warn("conversation probe failed quality check", "agent", agentID)
	}

	// Tier 4: Nudge loop — only when the session is recoverable.
	if ctx.Err() == nil && res.SessionID != "" {
		for i := range maxReportNudges {
			if ctx.Err() != nil {
				break
			}
			fbRes, _ := resumeForReport(ctx, agentID, spec, res.SessionID, nudgePrompt, sc)

			// Re-check tier 1 after the nudge resume.
			if sc.Reports != nil {
				if sub, ok := sc.Reports.TakeReport(agentID); ok && sub != "" {
					if out, pass := qualityPassOrTrim(sub, qualityCheck); pass {
						sc.Log.Warn("report recovered via SubmitReport after nudge",
							"agent", agentID, "nudge", i+1)
						return out, nil
					}
				}
			}
			// Re-check tier 3 after the nudge resume.
			if msg, err := finalMessage(sc, fbRes); err == nil {
				if out, pass := qualityPassOrTrim(msg, qualityCheck); pass {
					sc.Log.Warn("report recovered via conversation after nudge",
						"agent", agentID, "nudge", i+1)
					return out, nil
				}
			}
		}
	}

	// Tier 5: Fail closed.
	if runErr != nil {
		return "", fmt.Errorf("%s: %w", agentID, runErr)
	}
	return "", fmt.Errorf("%s: no valid report produced", agentID)
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
