package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
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
	var plan string
	var err error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		sink := SinkFromObserver(s.ID(), sc.Obs)
		res, err = sc.Exec.Run(ctx, spec, nil, sink)
		if err != nil {
			if attempt < maxAttempts {
				sc.Log.Warn("researcher attempt failed, retrying",
					"attempt", attempt, "err", err)
				sc.Obs.AgentStarted(s.ID(), s.Meta) // re-arm display
				continue
			}
			sc.Obs.AgentFailed(s.ID(), err)
			s.writeMeta(sc, res.SessionID, start, "failed", err, harness.TokenUsage{})
			return ResearchOutput{}, fmt.Errorf("research: %w", err)
		}
		var usedFallback bool
		plan, usedFallback, err = preferReport(sc, "researcher", res, true)
		if usedFallback {
			sc.Log.Warn("researcher: model produced text output instead of writing plan file; "+
				"model may have disobeyed plan-writing instructions", "session_id", res.SessionID)
		}
		if err != nil {
			if attempt < maxAttempts {
				sc.Log.Warn("researcher plan extraction failed, retrying",
					"attempt", attempt, "err", err)
				sc.Obs.AgentStarted(s.ID(), s.Meta)
				continue
			}
			sc.Obs.AgentFailed(s.ID(), err)
			s.writeMeta(sc, res.SessionID, start, "failed", err, res.Usage)
			return ResearchOutput{}, fmt.Errorf("research: read plan: %w", err)
		}
		break
	}

	// Integrity artifact: researcher draft markdown.
	if writeErr := sc.Artifacts.Write("researcher_draft.md", []byte(plan)); writeErr != nil {
		sc.Obs.AgentFailed(s.ID(), writeErr)
		return ResearchOutput{}, writeErr
	}

	s.writeMeta(sc, res.SessionID, start, "done", nil, res.Usage)
	sc.Obs.AgentDone(s.ID(), res.Usage)

	return ResearchOutput{
		DraftMarkdown: plan,
		SessionID:     res.SessionID,
		Usage:         res.Usage,
	}, nil
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
