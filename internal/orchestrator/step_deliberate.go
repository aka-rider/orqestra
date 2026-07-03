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
		sc.Log,
		agent.ArchitectPrompt(in.OriginalPrompt),
		in.OriginalPrompt,
		"architect",
	)

	archStart := time.Now()
	archSpec := s.ArchSpec
	archSpec.Prompt = archPrompt

	// No prior session/plan file exists for the initial pass — SnapshotPlanFile
	// is deliberately not called: there is nothing to compare tier 2 against,
	// so ReportHarvester.harvestReporter already treats this as
	// freshness-unverified-but-accepted (J35's documented first-write case).
	archHarvester := NewReportHarvester(sc, RoleReporter)
	archRes, planMarkdown, archProv, archErr := runReportAgent(ctx, sc, archHarvester, archSpec, s.ArchMeta, s.ArchAttempts)

	if archErr != nil {
		sc.Obs.AgentFailed("architect", archErr)
		s.writeArchMeta(sc, archRes.SessionID, archStart, "failed", archErr, archRes.Usage, ReportProvenance{})
		return PlanOutput{}, fmt.Errorf("architect: %w", archErr)
	}

	sc.Obs.ReportHarvested("architect", archProv)
	s.writeArchMeta(sc, archRes.SessionID, archStart, "done", nil, archRes.Usage, archProv)
	sc.Obs.AgentDone("architect", archRes.Usage)

	planSessionID := archRes.SessionID
	planFilePath := archRes.PlanFilePath

	if !s.HasCritic {
		return PlanOutput{
			Markdown:     planMarkdown,
			Warnings:     agent.CheckPlanHealth(planMarkdown),
			SessionID:    planSessionID,
			PlanFilePath: planFilePath,
		}, nil
	}

	// --- Multi-round critic/revision loop ---
	// Rounds=1 → one critic pass, Rounds=2 → two, etc.
	for round := 0; round < s.Rounds; round++ {
		var err error
		planMarkdown, planSessionID, planFilePath, err = s.runRound(ctx, planMarkdown, planSessionID, planFilePath, round, in.OriginalPrompt, sc)
		if err != nil {
			return PlanOutput{}, fmt.Errorf("round %d critic/revision failed: %w", round+1, err)
		}
	}

	return PlanOutput{
		Markdown:     planMarkdown,
		Warnings:     agent.CheckPlanHealth(planMarkdown),
		SessionID:    planSessionID,
		PlanFilePath: planFilePath,
	}, nil
}

// runRound executes one critic review + architect revision cycle (0-indexed).
//
// Error contract (returns (string, string, string, error)):
//
//	Critic error (fatal): return ("", "", "", criticErr) — loop breaks, error propagates.
//	Revision non-fatal (chat-only): return (planMarkdown, sessionID, planFilePath, nil) — loop continues with previous plan, session, and plan file path.
//	Success: return (revisedMarkdown, revRes.SessionID, revised plan file path, nil) — loop continues with new plan.
func (s *DeliberateStep) runRound(
	ctx context.Context,
	planMarkdown string,
	sessionID string,
	planFilePath string,
	roundNum int,
	originalPrompt string,
	sc StepContext,
) (string, string, string, error) {
	// --- Critic review ---
	sc.Obs.AgentStarted("critic", s.CriticMeta)
	sc.Obs.PhaseChanged(PhaseCritiquing)

	criticPrompt := guardPrompt(
		sc.Log,
		agent.CriticReviewPrompt(originalPrompt, planMarkdown),
		originalPrompt,
		"critic",
	)

	criticStart := time.Now()
	criticSpec := s.CriticSpec
	criticSpec.Prompt = criticPrompt

	// Critic never sets PlanMode, so this harvester's tier 2 (plan file) is
	// never consulted for it — no snapshot needed.
	criticHarvester := NewReportHarvester(sc, RoleReporter)
	criticRes, criticMarkdown, criticProv, criticErr := runReportAgent(ctx, sc, criticHarvester, criticSpec, s.CriticMeta, s.CriticAttempts)

	if criticErr != nil {
		sc.Obs.AgentFailed("critic", criticErr)
		writeMeta(sc, fmt.Sprintf("critic_meta_round%d.json", roundNum+1), "critic", s.CriticMeta, criticRes.SessionID, criticStart, "failed", criticErr, criticRes.Usage, ReportProvenance{})
		return "", "", "", fmt.Errorf("critic: %w", criticErr)
	}

	sc.Obs.ReportHarvested("critic", criticProv)
	sc.Artifacts.WriteBestEffort(fmt.Sprintf("critic_report_round%d.md", roundNum+1), []byte(criticMarkdown))
	writeMeta(sc, fmt.Sprintf("critic_meta_round%d.json", roundNum+1), "critic", s.CriticMeta, criticRes.SessionID, criticStart, "done", nil, criticRes.Usage, criticProv)
	sc.Obs.AgentDone("critic", criticRes.Usage)

	// --- Architect revision (critic feedback) ---
	sc.Obs.AgentStarted("architect", s.ArchMeta)

	revSpec := s.ArchSpec
	revSpec.Prompt = agent.CriticContinuePrompt(planMarkdown, criticMarkdown)
	revSpec.Resume = harness.ResumeSession(sessionID)
	// Revision completes by finishing its response, not by calling SubmitReport —
	// the default architect silence nudge references that tool, so give this
	// round its own text. SilenceSecs/MaxNudges stay inherited from ArchSpec.
	revSpec.SilenceGuard.NudgeText = agent.ArchitectRevisionSilenceNudge

	// J35: snapshot the plan file's PRE-invocation state using the prior
	// known session+path (this round resumes sessionID) so the harvester can
	// tell whether the architect actually rewrote it this turn, instead of
	// silently accepting whatever is on disk regardless of freshness.
	revHarvester := NewReportHarvester(sc, RoleReporter)
	if revSpec.PlanMode {
		revHarvester.SnapshotPlanFile(sessionID, planFilePath, sc.RepoPath)
	}

	revStart := time.Now()
	revRes, revised, revProv, revErr := runReportAgent(ctx, sc, revHarvester, revSpec, s.ArchMeta, 1)

	// chatOnlyFallback marks the J12 case: revision extraction failed with a
	// non-fatal (non-ctx) error, so the loop falls back to the previous plan.
	// Falling back is correct (fail-forward, do not change it) — but the meta
	// artifact must say "fallback", not claim a clean "done" for a revision
	// that produced nothing (J12: a failed revision was recorded as success).
	chatOnlyFallback := false
	if revErr != nil {
		// Distinguish fatal context/budget/loop errors from "chat-only, no plan produced".
		// Fatal: propagate. Chat-only: fall back to the existing plan.
		if ctx.Err() != nil {
			// Prefer the attributed cause (e.g. ErrUserCancelled) over the bare
			// ctx.Err() so the meta artifact and logs are self-explanatory.
			cause := context.Cause(ctx)
			sc.Obs.AgentFailed("architect", cause)
			writeMeta(sc, fmt.Sprintf("architect_critic_revision_meta_round%d.json", roundNum+1), string(s.ID()), s.ArchMeta, sessionID, revStart, "failed", cause, revRes.Usage, ReportProvenance{})
			return "", "", "", fmt.Errorf("architect critic revision: %w", cause)
		}
		// No report extracted — treat as chat-only continuation (no plan rewrite).
		sc.Log.Debug("architect critic revision: plan unchanged (chat continuation)", "err", revErr)
		revised = planMarkdown
		chatOnlyFallback = true
	} else {
		sc.Obs.ReportHarvested("architect", revProv)
	}
	if revised == "" {
		revised = planMarkdown
	}

	metaStatus := "done"
	var metaErr error
	if chatOnlyFallback {
		metaStatus = "fallback"
		metaErr = revErr
	}
	writeMeta(sc, fmt.Sprintf("architect_critic_revision_meta_round%d.json", roundNum+1), string(s.ID()), s.ArchMeta, sessionID, revStart, metaStatus, metaErr, revRes.Usage, revProv)
	sc.Obs.AgentDone("architect", revRes.Usage)

	// Only advance the session ID (and plan file path) when the plan actually
	// changed; keep the originals when falling back so the next round resumes
	// from the last known-good plan's session and file.
	if revised != "" && revised != planMarkdown {
		nextPlanFilePath := revRes.PlanFilePath
		if nextPlanFilePath == "" {
			nextPlanFilePath = planFilePath
		}
		return revised, revRes.SessionID, nextPlanFilePath, nil
	}
	return planMarkdown, sessionID, planFilePath, nil
}

func (s *DeliberateStep) writeArchMeta(sc StepContext, sid string, start time.Time, status string, err error, usage harness.TokenUsage, prov ReportProvenance) {
	writeMeta(sc, "architect_meta.json", string(s.ID()), s.ArchMeta, sid, start, status, err, usage, prov)
}

func writeMeta(sc StepContext, filename, agentID string, meta AgentMeta, sessionID string, start time.Time, status string, err error, usage harness.TokenUsage, prov ReportProvenance) {
	m := StepMeta{
		AgentID:              agentID,
		ModelRef:             meta.ModelRef,
		ModelDisplay:         meta.ModelDisplay,
		Provider:             meta.Provider,
		ContextWindow:        meta.ContextWindow,
		ClaudeSessionID:      sessionID,
		ClaudeSessionLogPath: resolveSessionLogPath(sc, sessionID),
		StartTime:            start,
		EndTime:              time.Now(),
		Status:               status,
		InputTokens:          usage.Input,
		OutputTokens:         usage.Output,
		ReportTier:           prov.Tier,
		ReportSource:         prov.Source,
		ReportDetail:         prov.Detail,
		ReportRejected:       prov.Rejected,
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
