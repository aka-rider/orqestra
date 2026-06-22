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
	Rounds         int
}

func (s *DeliberateStep) ID() AgentID { return "architect" }

func (s *DeliberateStep) Run(ctx context.Context, in DeliberateInput, sc StepContext) (PlanOutput, error) {
	// --- Architect initial pass ---
	sc.Obs.AgentStarted("architect", s.ArchMeta)
	sc.Obs.PhaseChanged(PhasePlanning)

	archPrompt := guardPrompt(
		agent.ArchitectPrompt(in.OriginalPrompt),
		in.OriginalPrompt,
		"architect",
	)

	archStart := time.Now()
	archSpec := s.ArchSpec
	archSpec.Prompt = archPrompt

	archRes, planMarkdown, archErr := runReportAgent(ctx, sc, archSpec, s.ArchMeta, s.ArchAttempts)

	if archErr != nil {
		sc.Obs.AgentFailed("architect", archErr)
		s.writeArchMeta(sc, archRes.SessionID, archStart, "failed", archErr, archRes.Usage)
		return PlanOutput{}, fmt.Errorf("architect: %w", archErr)
	}

	s.writeArchMeta(sc, archRes.SessionID, archStart, "done", nil, archRes.Usage)
	sc.Obs.AgentDone("architect", archRes.Usage)

	planSessionID := archRes.SessionID

	if !s.HasCritic {
		return PlanOutput{
			Markdown:  planMarkdown,
			Warnings:  agent.CheckPlanHealth(planMarkdown),
			SessionID: planSessionID,
		}, nil
	}

	// --- Multi-round critic/revision loop ---
	// Rounds=1 → one critic pass, Rounds=2 → two, etc.
	for round := 0; round < s.Rounds; round++ {
		var err error
		planMarkdown, planSessionID, err = s.runRound(ctx, planMarkdown, planSessionID, round, in.OriginalPrompt, sc)
		if err != nil {
			return PlanOutput{}, fmt.Errorf("round %d critic/revision failed: %w", round+1, err)
		}
	}

	return PlanOutput{
		Markdown:  planMarkdown,
		Warnings:  agent.CheckPlanHealth(planMarkdown),
		SessionID: planSessionID,
	}, nil
}

// runRound executes one critic review + architect revision cycle (0-indexed).
//
// Error contract (returns (string, string, error)):
//
//	Critic error (fatal): return ("", "", criticErr) — loop breaks, error propagates.
//	Revision non-fatal (chat-only): return (planMarkdown, sessionID, nil) — loop continues with previous plan and session.
//	Success: return (revisedMarkdown, revRes.SessionID, nil) — loop continues with new plan.
func (s *DeliberateStep) runRound(
	ctx context.Context,
	planMarkdown string,
	sessionID string,
	roundNum int,
	originalPrompt string,
	sc StepContext,
) (string, string, error) {
	// --- Critic review ---
	sc.Obs.AgentStarted("critic", s.CriticMeta)
	sc.Obs.PhaseChanged(PhaseCritiquing)

	criticPrompt := guardPrompt(
		agent.CriticReviewPrompt(originalPrompt, planMarkdown),
		originalPrompt,
		"critic",
	)

	criticStart := time.Now()
	criticSpec := s.CriticSpec
	criticSpec.Prompt = criticPrompt

	criticRes, criticMarkdown, criticErr := runReportAgent(ctx, sc, criticSpec, s.CriticMeta, s.CriticAttempts)

	if criticErr != nil {
		sc.Obs.AgentFailed("critic", criticErr)
		writeMeta(sc, fmt.Sprintf("critic_meta_round%d.json", roundNum+1), "critic", s.CriticMeta, criticRes.SessionID, criticStart, "failed", criticErr, criticRes.Usage)
		return "", "", fmt.Errorf("critic: %w", criticErr)
	}

	sc.Artifacts.WriteBestEffort(fmt.Sprintf("critic_report_round%d.md", roundNum+1), []byte(criticMarkdown))
	writeMeta(sc, fmt.Sprintf("critic_meta_round%d.json", roundNum+1), "critic", s.CriticMeta, criticRes.SessionID, criticStart, "done", nil, criticRes.Usage)
	sc.Obs.AgentDone("critic", criticRes.Usage)

	// --- Architect revision (critic feedback) ---
	sc.Obs.AgentStarted("architect", s.ArchMeta)

	revSpec := s.ArchSpec
	revSpec.Prompt = agent.CriticContinuePrompt(planMarkdown, criticMarkdown)
	revSpec.Resume = harness.ResumeSession(sessionID)
	revSpec.SilenceGuard = harness.SilenceGuardSpec{} // revision is text-only; silence nudge loops on it

	revStart := time.Now()
	revRes, revised, revErr := runReportAgent(ctx, sc, revSpec, s.ArchMeta, 1)

	if revErr != nil {
		// Distinguish fatal context/budget/loop errors from "chat-only, no plan produced".
		// Fatal: propagate. Chat-only: fall back to the existing plan.
		if ctx.Err() != nil {
			sc.Obs.AgentFailed("architect", ctx.Err())
			writeMeta(sc, fmt.Sprintf("architect_critic_revision_meta_round%d.json", roundNum+1), string(s.ID()), s.ArchMeta, sessionID, revStart, "failed", ctx.Err(), revRes.Usage)
			return "", "", fmt.Errorf("architect critic revision: %w", ctx.Err())
		}
		// No report extracted — treat as chat-only continuation (no plan rewrite).
		sc.Log.Debug("architect critic revision: plan unchanged (chat continuation)", "err", revErr)
		revised = planMarkdown
	}
	if revised == "" {
		revised = planMarkdown
	}

	writeMeta(sc, fmt.Sprintf("architect_critic_revision_meta_round%d.json", roundNum+1), string(s.ID()), s.ArchMeta, sessionID, revStart, "done", nil, revRes.Usage)
	sc.Obs.AgentDone("architect", revRes.Usage)

	// Only advance the session ID when the plan actually changed; keep the original when
	// falling back so the next round resumes from the last known-good plan's session.
	if revised != "" && revised != planMarkdown {
		return revised, revRes.SessionID, nil
	}
	return planMarkdown, sessionID, nil
}

func (s *DeliberateStep) writeArchMeta(sc StepContext, sid string, start time.Time, status string, err error, usage harness.TokenUsage) {
	writeMeta(sc, "architect_meta.json", string(s.ID()), s.ArchMeta, sid, start, status, err, usage)
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
