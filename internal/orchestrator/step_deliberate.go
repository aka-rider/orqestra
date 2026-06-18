package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/harness"
)

// DeliberateStep runs architect planning followed by critic review and revision.
// If CriticSpec is zero (no critic configured), only the architect runs.
type DeliberateStep struct {
	ArchSpec     harness.ProcessSpec
	ArchMeta     AgentMeta
	ArchAttempts int

	CriticSpec     harness.ProcessSpec
	CriticMeta     AgentMeta
	CriticAttempts int
	HasCritic      bool
}

func (s *DeliberateStep) ID() AgentID { return "architect" }

func (s *DeliberateStep) Run(ctx context.Context, in DeliberateInput, sc StepContext) (PlanOutput, error) {
	// --- Architect initial pass ---
	sc.Obs.AgentStarted("architect", s.ArchMeta)
	sc.Obs.PhaseChanged(PhasePlanning)

	archPrompt := guardPrompt(
		agent.ArchitectPrompt(in.OriginalPrompt, in.Draft),
		in.OriginalPrompt,
		"architect",
	)

	archStart := time.Now()
	archSpec := s.ArchSpec
	archSpec.Prompt = archPrompt

	maxArch := s.ArchAttempts
	if maxArch < 1 {
		maxArch = 1
	}

	var archRes harness.RunResult
	var planMarkdown string
	var err error

	for attempt := 1; attempt <= maxArch; attempt++ {
		sink := SinkFromObserver("architect", sc.Obs)
		archRes, err = sc.Exec.Run(ctx, archSpec, nil, sink)
		if err != nil {
			if attempt < maxArch {
				sc.Log.Warn("architect attempt failed, retrying", "attempt", attempt, "err", err)
				sc.Obs.AgentStarted("architect", s.ArchMeta)
				continue
			}
			sc.Obs.AgentFailed("architect", err)
			s.writeArchMeta(sc, archRes.SessionID, archStart, "failed", err, harness.TokenUsage{})
			return PlanOutput{}, fmt.Errorf("architect: %w", err)
		}
		var usedFallback bool
		planMarkdown, usedFallback, err = agent.ReadPlan(archRes.SessionID, archRes.PlanFilePath, sc.RepoPath, archRes.Output)
		if usedFallback {
			sc.Log.Warn("architect: model produced text output instead of writing plan file; "+
				"model may have disobeyed plan-writing instructions", "session_id", archRes.SessionID)
		}
		if err != nil {
			if attempt < maxArch {
				sc.Log.Warn("architect plan extraction failed, retrying", "attempt", attempt, "err", err)
				sc.Obs.AgentStarted("architect", s.ArchMeta)
				continue
			}
			sc.Obs.AgentFailed("architect", err)
			s.writeArchMeta(sc, archRes.SessionID, archStart, "failed", err, archRes.Usage)
			return PlanOutput{}, fmt.Errorf("architect: read plan: %w", err)
		}
		break
	}

	s.writeArchMeta(sc, archRes.SessionID, archStart, "done", nil, archRes.Usage)
	sc.Obs.AgentDone("architect", archRes.Usage)

	planWarnings := agent.CheckPlanHealth(planMarkdown)
	planSessionID := archRes.SessionID

	if !s.HasCritic {
		return PlanOutput{
			Markdown:  planMarkdown,
			Warnings:  planWarnings,
			SessionID: planSessionID,
		}, nil
	}

	// --- Critic review ---
	sc.Obs.AgentStarted("critic", s.CriticMeta)
	sc.Obs.PhaseChanged(PhaseCritiquing)

	criticPrompt := guardPrompt(
		agent.CriticReviewPrompt(in.OriginalPrompt, planMarkdown),
		in.OriginalPrompt,
		"critic",
	)

	criticStart := time.Now()
	criticSpec := s.CriticSpec
	criticSpec.Prompt = criticPrompt

	maxCritic := s.CriticAttempts
	if maxCritic < 1 {
		maxCritic = 1
	}

	var criticRes harness.RunResult
	var criticMarkdown string

	for attempt := 1; attempt <= maxCritic; attempt++ {
		sink := SinkFromObserver("critic", sc.Obs)
		criticRes, err = sc.Exec.Run(ctx, criticSpec, nil, sink)
		if err != nil {
			if attempt < maxCritic {
				sc.Log.Warn("critic attempt failed, retrying", "attempt", attempt, "err", err)
				sc.Obs.AgentStarted("critic", s.CriticMeta)
				continue
			}
			sc.Obs.AgentFailed("critic", err)
			s.writeCriticMeta(sc, criticRes.SessionID, criticStart, "failed", err, harness.TokenUsage{})
			return PlanOutput{}, fmt.Errorf("critic: %w", err)
		}
		var criticFallback bool
		criticMarkdown, criticFallback, err = agent.ReadPlan(criticRes.SessionID, criticRes.PlanFilePath, sc.RepoPath, criticRes.Output)
		if criticFallback {
			sc.Log.Warn("critic: model produced text output instead of writing plan file; "+
				"model may have disobeyed plan-writing instructions", "session_id", criticRes.SessionID)
		}
		if err != nil {
			if attempt < maxCritic {
				sc.Log.Warn("critic plan extraction failed, retrying", "attempt", attempt, "err", err)
				sc.Obs.AgentStarted("critic", s.CriticMeta)
				continue
			}
			sc.Obs.AgentFailed("critic", err)
			s.writeCriticMeta(sc, criticRes.SessionID, criticStart, "failed", err, criticRes.Usage)
			return PlanOutput{}, fmt.Errorf("critic: read report: %w", err)
		}
		break
	}

	sc.Artifacts.WriteBestEffort("critic_report.md", []byte(criticMarkdown))
	s.writeCriticMeta(sc, criticRes.SessionID, criticStart, "done", nil, criticRes.Usage)
	sc.Obs.AgentDone("critic", criticRes.Usage)

	// --- Architect second pass (critic feedback) ---
	sc.Obs.AgentStarted("architect", s.ArchMeta)

	revSpec := s.ArchSpec
	revSpec.Prompt = agent.CriticContinuePrompt(planMarkdown, criticMarkdown)
	revSpec.Resume = harness.ResumeSession(planSessionID)

	revStart := time.Now()
	revSink := SinkFromObserver("architect", sc.Obs)
	revRes, revErr := sc.Exec.Run(ctx, revSpec, nil, revSink)

	if revErr != nil {
		sc.Obs.AgentFailed("architect", revErr)
		s.writeArchRevMeta(sc, planSessionID, revStart, "failed", revErr, harness.TokenUsage{})
		return PlanOutput{}, fmt.Errorf("architect critic revision: %w", revErr)
	}

	revised, _, readErr := agent.ReadPlan(revRes.SessionID, revRes.PlanFilePath, sc.RepoPath, "")
	if readErr != nil {
		// Continuation may have been chat-only (no plan rewrite) — treat as no change.
		sc.Log.Debug("architect critic revision: plan unchanged (chat continuation)", "err", readErr)
		revised = planMarkdown
	}

	s.writeArchRevMeta(sc, planSessionID, revStart, "done", nil, revRes.Usage)
	sc.Obs.AgentDone("architect", revRes.Usage)

	if revised != "" && revised != planMarkdown {
		planMarkdown = revised
	}

	return PlanOutput{
		Markdown:  planMarkdown,
		Warnings:  agent.CheckPlanHealth(planMarkdown),
		SessionID: planSessionID,
	}, nil
}

func (s *DeliberateStep) writeArchMeta(sc StepContext, sid string, start time.Time, status string, err error, usage harness.TokenUsage) {
	writeMeta(sc, "architect_meta.json", string(s.ID()), s.ArchMeta, sid, start, status, err, usage)
}

func (s *DeliberateStep) writeCriticMeta(sc StepContext, sid string, start time.Time, status string, err error, usage harness.TokenUsage) {
	writeMeta(sc, "critic_meta.json", "critic", s.CriticMeta, sid, start, status, err, usage)
}

func (s *DeliberateStep) writeArchRevMeta(sc StepContext, sid string, start time.Time, status string, err error, usage harness.TokenUsage) {
	writeMeta(sc, "architect_critic_revision_meta.json", string(s.ID()), s.ArchMeta, sid, start, status, err, usage)
}

func writeMeta(sc StepContext, filename, agentID string, meta AgentMeta, sessionID string, start time.Time, status string, err error, usage harness.TokenUsage) {
	m := StepMeta{
		AgentID:         agentID,
		ModelRef:        meta.ModelRef,
		ModelDisplay:    meta.ModelDisplay,
		Provider:        meta.Provider,
		ContextWindow:   meta.ContextWindow,
		ClaudeSessionID: sessionID,
		StartTime:       start,
		EndTime:         time.Now(),
		Status:          status,
		InputTokens:     usage.Input,
		OutputTokens:    usage.Output,
	}
	if err != nil {
		m.Error = err.Error()
	}
	data, jsonErr := json.MarshalIndent(m, "", "  ")
	if jsonErr != nil {
		return
	}
	sc.Artifacts.WriteBestEffort(filename, data)
}
