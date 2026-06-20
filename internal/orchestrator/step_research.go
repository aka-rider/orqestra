package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/harness"
)

// ResearchStep executes the researcher agent and returns a draft markdown plan.
type ResearchStep struct {
	Spec     harness.ProcessSpec
	Meta     AgentMeta
	Attempts int // retry count; 0 → 1
}

func (s *ResearchStep) ID() AgentID { return "researcher" }

const researcherNudgePrompt = "[Orchestrator] Submit your FACT REPORT now by calling " +
	"mcp__orqestra__SubmitReport with the full markdown in the \"report\" argument. " +
	"Required sections: ## Goal, ## Codebase Facts, ## Constraints Discovered, ## Gotchas. " +
	"Report what exists; do not propose changes. Partial is acceptable."

func (s *ResearchStep) Run(ctx context.Context, in ResearchInput, sc StepContext) (ResearchOutput, error) {
	sc.Obs.PhaseChanged(PhaseResearching)
	sc.Obs.AgentStarted(s.ID(), s.Meta)

	maxAttempts := s.Attempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	start := time.Now()
	spec := s.Spec
	spec.Prompt = guardPrompt(agent.ResearcherPrompt(in.Prompt), in.Prompt, "researcher")

	var res harness.RunResult
	var report string
	var runErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		sink := SinkFromObserver(s.ID(), sc.Obs)
		res, runErr = sc.Exec.Run(ctx, spec, nil, sink)
		if runErr != nil {
			if attempt < maxAttempts {
				sc.Log.Warn("researcher attempt failed, retrying",
					"attempt", attempt, "err", runErr)
				sc.Obs.AgentStarted(s.ID(), s.Meta)
				continue
			}
			break
		}
		break
	}

	// Researcher runs outside plan mode (default permission): extract via the
	// submission → conversation → nudge ladder, gate on the role-adherence spectrum,
	// then inject the verbatim ## User Task section the orchestrator owns.
	var extractErr error
	report, extractErr = extractReport(ctx, "researcher", spec, res, runErr, researcherNudgePrompt, researchSpectrum.check, false, sc)
	if extractErr == nil {
		report = ensureUserTask(in.Prompt, report)
	}

	if extractErr != nil {
		sc.Obs.AgentFailed(s.ID(), extractErr)
		s.writeMeta(sc, res.SessionID, start, "failed", extractErr, res.Usage)
		return ResearchOutput{}, extractErr
	}

	// Integrity artifact: researcher draft markdown.
	if writeErr := sc.Artifacts.Write("researcher_draft.md", []byte(report)); writeErr != nil {
		sc.Obs.AgentFailed(s.ID(), writeErr)
		return ResearchOutput{}, writeErr
	}

	s.writeMeta(sc, res.SessionID, start, "done", nil, res.Usage)
	sc.Obs.AgentDone(s.ID(), res.Usage)

	return ResearchOutput{
		DraftMarkdown: report,
		SessionID:     res.SessionID,
		Usage:         res.Usage,
	}, nil
}

// ensureUserTask guarantees the researcher report opens with a verbatim ## User Task
// section. The orchestrator owns the task text, so it injects this deterministically
// rather than trusting the model to echo it (historically the most-missed section).
func ensureUserTask(task, report string) string {
	if strings.Contains(strings.ToLower(report), "## user task") {
		return report
	}
	return "## User Task\n\n" + strings.TrimSpace(task) + "\n\n" + report
}

func (s *ResearchStep) writeMeta(sc StepContext, sessionID string, start time.Time, status string, err error, usage harness.TokenUsage) {
	meta := StepMeta{
		AgentID:         string(s.ID()),
		ModelRef:        s.Meta.ModelRef,
		ModelDisplay:    s.Meta.ModelDisplay,
		Provider:        s.Meta.Provider,
		ContextWindow:   s.Meta.ContextWindow,
		ClaudeSessionID: sessionID,
		StartTime:       start,
		EndTime:         time.Now(),
		Status:          status,
		InputTokens:     usage.Input,
		OutputTokens:    usage.Output,
	}
	if err != nil {
		meta.Error = err.Error()
	}
	data, jsonErr := json.MarshalIndent(meta, "", "  ")
	if jsonErr != nil {
		return
	}
	sc.Artifacts.WriteBestEffort("researcher_meta.json", data)
}
