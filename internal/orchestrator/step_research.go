package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/xiii/orqestra/internal/harness"
)

// ResearchStep executes the researcher agent and returns a draft markdown plan.
type ResearchStep struct {
	Spec     harness.ProcessSpec
	Meta     AgentMeta
	Attempts int // retry count; 0 → 1
}

func (s *ResearchStep) ID() AgentID { return "researcher" }

const researcherFallbackPrompt = "[Orchestrator] Your session did not produce a plan file. " +
	"Write your gathered findings as your plan now. " +
	"Required sections: ## Goal, ## Codebase Facts, ## Constraints Discovered, ## Gotchas. " +
	"Partial is acceptable."

func (s *ResearchStep) Run(ctx context.Context, in ResearchInput, sc StepContext) (ResearchOutput, error) {
	sc.Obs.PhaseChanged(PhaseResearching)
	sc.Obs.AgentStarted(s.ID(), s.Meta)

	maxAttempts := s.Attempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	start := time.Now()
	spec := s.Spec
	spec.Prompt = guardPrompt(in.Prompt, in.Prompt, "researcher")

	var res harness.RunResult
	var report string
	var usedFallback bool
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

	report, usedFallback, runErr = extractPlan(ctx, "researcher", spec, res, runErr, researcherFallbackPrompt, checkResearchReport, sc)
	if runErr == nil && !strings.Contains(strings.ToLower(report), "## user task") {
		sc.Log.Warn("research report missing ## User Task section (canary)", "session_id", res.SessionID)
	}
	if usedFallback {
		sc.Log.Warn("researcher: model produced text output instead of writing plan file; "+
			"model may have disobeyed plan-writing instructions", "session_id", res.SessionID)
	}

	if runErr != nil {
		sc.Obs.AgentFailed(s.ID(), runErr)
		s.writeMeta(sc, res.SessionID, start, "failed", runErr, res.Usage)
		return ResearchOutput{}, runErr
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

// checkResearchReport returns an error if the report is empty, contains only
// markdown headers, or is missing a required section. It does not check for the
// ## User Task section — that is a canary warning logged by the caller.
func checkResearchReport(report string) error {
	trimmed := strings.TrimSpace(report)
	if trimmed == "" {
		return fmt.Errorf("report is empty")
	}
	required := []string{"## Goal", "## Codebase Facts", "## Constraints Discovered", "## Gotchas"}
	lower := strings.ToLower(trimmed)
	for _, sec := range required {
		if !strings.Contains(lower, strings.ToLower(sec)) {
			return fmt.Errorf("report missing required section %q", sec)
		}
	}
	contentLines := 0
	for _, line := range strings.Split(trimmed, "\n") {
		l := strings.TrimSpace(line)
		if l == "" || (len(l) > 0 && l[0] == '#') {
			continue
		}
		contentLines++
	}
	if contentLines < 5 {
		return fmt.Errorf("report has only %d non-header content lines (minimum 5)", contentLines)
	}
	return nil
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
