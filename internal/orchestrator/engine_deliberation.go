package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/harness"
)

// deliberationResult holds the output of the deliberation phase.
type deliberationResult struct {
	PlanMarkdown  string
	PlanWarnings  []string
	PlanSessionID string
	CriticReport  string
}

// runDeliberation executes the planning, critic review, and architect revision loop.
// It takes the draft markdown from research and returns the final plan.
// Returns nil, false on failure (error already emitted).
func (e *Engine) runDeliberation(
	ctx context.Context,
	session agent.SessionDir,
	emit func(Event),
	logger *slog.Logger,
	agentStart map[string]time.Time,
	stream *streamCapture,
	streamOut chan<- harness.Event,
	draftMarkdown string,
	cwd string,
	inputPrompt string,
	archMeta AgentMeta,
	critMeta AgentMeta,
) (*deliberationResult, bool) {
	architectPlanner := agent.NewPlanner(e.Runners.Architect, e.Config.Architect.SystemPrompt)
	architectAttempt := 0
	var planSessionID string

	// --- Initial Planning ---
	architectAttempt++
	logAgentEvent(logger, "agent_started", "architect", architectAttempt, harness.TokenUsage{}, nil, agentStart)
	emit(Event{Type: EventAgentStarted, AgentID: "architect", Meta: resolveAgentMeta(e.Config, e.Config.Architect.Model)})
	stream.SetAgent("architect")
	emit(Event{Type: EventPhaseChange, Phase: PhasePlanning})
	logger.Info("phase", "phase", string(PhasePlanning))

	planStart := time.Now()
	architectAttempts := e.Config.Retry.ArchitectAttempts
	if architectAttempts < 1 {
		architectAttempts = 1
	}

	architectPrompt := guardPrompt(agent.ArchitectPrompt(inputPrompt, draftMarkdown), inputPrompt, "architect")

	var planUsage harness.TokenUsage
	var planMarkdown string
	var planErr error

	for attempt := 1; attempt <= architectAttempts; attempt++ {
		var aResult agent.PlanResult
		aResult, planErr = runPlanner(ctx, architectPlanner, architectPrompt, stream, streamOut)
		if planErr == nil {
			planSessionID = aResult.SessionID
			planUsage = aResult.Usage
			planMarkdown = aResult.Plan
			break
		}
		if attempt < architectAttempts {
			slog.Warn("architect attempt failed, retrying", "attempt", attempt, "err", planErr)
			stream.SetAgent("architect")
		}
	}
	if planErr != nil {
		e.failArchitect(session, planSessionID, logger, planStart, "architect", architectAttempt, planErr, architectPlanner, cwd, stream, streamOut, emit)
		return nil, false
	}

	archLogCopy, cpErr := copySessionLog(session, cwd, planSessionID, "architect_session.jsonl")
	if cpErr != nil {
		logger.Warn("copy session log", "agent", "architect", "err", cpErr)
	}
	archPlanFilePath := ""
	if archLogCopy != "" {
		archPlanFilePath, _ = harness.ExtractPlanFilePath(archLogCopy)
	}
	writeArtifactJSON(session, "architect_meta.json", agent.StepMeta{
		AgentID: "architect", ModelRef: e.Config.Architect.Model, StartTime: planStart, EndTime: time.Now(),
		ModelDisplay: archMeta.ModelDisplay, Provider: archMeta.Provider, ContextWindow: archMeta.ContextWindow,
		ClaudeSessionID: planSessionID, Status: "done",
		InputTokens: planUsage.Input, OutputTokens: planUsage.Output,
		ClaudeProjectPath:    claudeProjectPath(session),
		ClaudeSessionLogPath: archLogCopy,
		ClaudePlanFilePath:   archPlanFilePath,
	})
	logClaudeSession(logger, "architect", architectAttempt, planSessionID, archLogCopy, session)
	logAgentEvent(logger, "agent_done", "architect", architectAttempt, planUsage, nil, agentStart)
	emit(Event{Type: EventAgentDone, AgentID: "architect",
		InputTokens: planUsage.Input, OutputTokens: planUsage.Output})

	planWarnings := agent.CheckPlanHealth(planMarkdown)

	// --- Critic Review + Architect Revision ---
	criticReportMarkdown := ""
	if e.Runners.Critic != nil {
		report, ok := e.runCriticReview(ctx, session, emit, logger, agentStart, stream, streamOut,
			inputPrompt, planMarkdown, planSessionID, architectPlanner,
			cwd, archMeta, critMeta)
		if !ok {
			return nil, false
		}
		planMarkdown = report.PlanMarkdown
		planWarnings = report.PlanWarnings
		criticReportMarkdown = report.CriticReport
		planSessionID = report.PlanSessionID
	}

	return &deliberationResult{
		PlanMarkdown:  planMarkdown,
		PlanWarnings:  planWarnings,
		PlanSessionID: planSessionID,
		CriticReport:  criticReportMarkdown,
	}, true
}

// criticPhaseResult holds the output of the critic review + revision phase.
type criticPhaseResult struct {
	PlanMarkdown  string
	PlanWarnings  []string
	PlanSessionID string
	CriticReport  string
}

// runCriticReview executes critic review and architect revision.
func (e *Engine) runCriticReview(
	ctx context.Context,
	session agent.SessionDir,
	emit func(Event),
	logger *slog.Logger,
	agentStart map[string]time.Time,
	stream *streamCapture,
	streamOut chan<- harness.Event,
	inputPrompt string,
	planMarkdown string,
	planSessionID string,
	architectPlanner *agent.Planner,
	cwd string,
	archMeta AgentMeta,
	critMeta AgentMeta,
) (*criticPhaseResult, bool) {
	// --- Critic Review ---
	criticPlanner := agent.NewPlanner(e.Runners.Critic, e.Config.Critic.SystemPrompt)
	emit(Event{Type: EventPhaseChange, Phase: PhaseCritiquing})
	logger.Info("phase", "phase", string(PhaseCritiquing))
	logAgentEvent(logger, "agent_started", "critic", 1, harness.TokenUsage{}, nil, agentStart)
	emit(Event{Type: EventAgentStarted, AgentID: "critic", Meta: resolveAgentMeta(e.Config, e.Config.Critic.Model)})
	stream.SetAgent("critic")

	criticStart := time.Now()
	criticAttempts := e.Config.Retry.CriticAttempts
	if criticAttempts < 1 {
		criticAttempts = 1
	}

	criticPrompt := guardPrompt(agent.CriticReviewPrompt(inputPrompt, planMarkdown), inputPrompt, "critic")

	var criticMarkdown string
	var criticUsage harness.TokenUsage
	var criticSessionID string
	var criticStreamFallback bool
	var criticErr error

	for attempt := 1; attempt <= criticAttempts; attempt++ {
		var cResult agent.PlanResult
		cResult, criticErr = runPlanner(ctx, criticPlanner, criticPrompt, stream, streamOut)
		if criticErr == nil {
			criticMarkdown = cResult.Plan
			criticUsage = cResult.Usage
			criticSessionID = cResult.SessionID
			criticStreamFallback = cResult.StreamFallback
			break
		}
		if attempt < criticAttempts {
			slog.Warn("critic attempt failed, retrying", "attempt", attempt, "err", criticErr)
			stream.SetAgent("critic")
		}
	}
	if criticErr != nil {
		criticLogCopy, cpErr := copySessionLog(session, cwd, criticSessionID, "critic_session.jsonl")
		if cpErr != nil {
			logger.Warn("copy session log", "agent", "critic", "err", cpErr)
		}
		writeArtifactJSON(session, "critic_meta.json", agent.StepMeta{
			AgentID: "critic", ModelRef: e.Config.Critic.Model, StartTime: criticStart, EndTime: time.Now(),
			ModelDisplay: critMeta.ModelDisplay, Provider: critMeta.Provider, ContextWindow: critMeta.ContextWindow,
			ClaudeSessionID: criticSessionID, Status: "failed", Error: criticErr.Error(),
			ClaudeProjectPath:    claudeProjectPath(session),
			ClaudeSessionLogPath: criticLogCopy,
		})
		logClaudeSession(logger, "critic", 1, criticSessionID, criticLogCopy, session)
		logAgentEvent(logger, "agent_failed", "critic", 1, harness.TokenUsage{}, criticErr, agentStart)
		emit(Event{Type: EventAgentFailed, AgentID: "critic", Err: criticErr})
		emit(Event{Type: EventError, Err: fmt.Errorf("critic review: %w", criticErr)})
		return nil, false
	}

	criticLogCopy, cpErr := copySessionLog(session, cwd, criticSessionID, "critic_session.jsonl")
	if cpErr != nil {
		logger.Warn("copy session log", "agent", "critic", "err", cpErr)
	}
	if criticStreamFallback {
		logger.Warn("critic report recovered from stream output (plan file was not written)",
			"session_id", criticSessionID)
	}
	criticPlanSource := "plan_file"
	if criticStreamFallback {
		criticPlanSource = "stream_fallback"
	}
	writeArtifact(session, "critic_report.md", criticMarkdown)
	writeArtifactJSON(session, "critic_meta.json", agent.StepMeta{
		AgentID: "critic", ModelRef: e.Config.Critic.Model, StartTime: criticStart, EndTime: time.Now(),
		ModelDisplay: critMeta.ModelDisplay, Provider: critMeta.Provider, ContextWindow: critMeta.ContextWindow,
		ClaudeSessionID: criticSessionID, Status: "done", PlanSource: criticPlanSource,
		InputTokens: criticUsage.Input, OutputTokens: criticUsage.Output,
		ClaudeProjectPath:    claudeProjectPath(session),
		ClaudeSessionLogPath: criticLogCopy,
	})
	logClaudeSession(logger, "critic", 1, criticSessionID, criticLogCopy, session)
	logAgentEvent(logger, "agent_done", "critic", 1, criticUsage, nil, agentStart)
	emit(Event{Type: EventAgentDone, AgentID: "critic",
		InputTokens: criticUsage.Input, OutputTokens: criticUsage.Output})

	// --- Architect Second Pass (critic feedback) ---
	architectAttempt := 2
	logAgentEvent(logger, "agent_started", "architect", architectAttempt, harness.TokenUsage{}, nil, agentStart)
	emit(Event{Type: EventAgentStarted, AgentID: "architect", Meta: resolveAgentMeta(e.Config, e.Config.Architect.Model)})
	stream.SetAgent("architect")
	revStart := time.Now()

	critBaseline, critBaselineErr := architectPlanner.ExtractPlan(ctx)
	if critBaselineErr != nil {
		slog.Debug("could not snapshot plan file baseline before critic revision", "err", critBaselineErr)
	}
	critRevResult, revErr := continuePlanner(ctx, architectPlanner, planSessionID, agent.CriticContinuePrompt(planMarkdown, criticMarkdown), stream, streamOut)
	chatResponse := critRevResult.Chat
	revisedPlan := agent.DetectPlanRevision(critRevResult.Plan, critBaseline, critBaselineErr, planMarkdown)
	revisedUsage := critRevResult.Usage
	_ = chatResponse

	if revErr != nil {
		archCritRevLogCopy, cpErr := copySessionLog(session, cwd, planSessionID, "architect_critic_revision_session.jsonl")
		if cpErr != nil {
			logger.Warn("copy session log", "agent", "architect", "err", cpErr)
		}
		archCritRevPlanFilePath := ""
		if archCritRevLogCopy != "" {
			archCritRevPlanFilePath, _ = harness.ExtractPlanFilePath(archCritRevLogCopy)
		}
		writeArtifactJSON(session, fmt.Sprintf("architect_critic_revision_%d_meta.json", architectAttempt), agent.StepMeta{
			AgentID: "architect", ModelRef: e.Config.Architect.Model,
			ModelDisplay: archMeta.ModelDisplay, Provider: archMeta.Provider, ContextWindow: archMeta.ContextWindow,
			StartTime: revStart, EndTime: time.Now(),
			ClaudeSessionID: planSessionID, Status: "failed", Error: revErr.Error(),
			ClaudeProjectPath:    claudeProjectPath(session),
			ClaudeSessionLogPath: archCritRevLogCopy,
			ClaudePlanFilePath:   archCritRevPlanFilePath,
		})
		logClaudeSession(logger, "architect", architectAttempt, planSessionID, archCritRevLogCopy, session)
		logAgentEvent(logger, "agent_failed", "architect", architectAttempt, harness.TokenUsage{}, revErr, agentStart)
		emit(Event{Type: EventAgentFailed, AgentID: "architect", Err: revErr})
		emit(Event{Type: EventError, Err: fmt.Errorf("architect critic revision: %w", revErr)})
		return nil, false
	}

	archCritRevLogCopy, cpErr := copySessionLog(session, cwd, planSessionID, "architect_critic_revision_session.jsonl")
	if cpErr != nil {
		logger.Warn("copy session log", "agent", "architect", "err", cpErr)
	}
	archCritRevPlanFilePath := ""
	if archCritRevLogCopy != "" {
		archCritRevPlanFilePath, _ = harness.ExtractPlanFilePath(archCritRevLogCopy)
	}
	writeArtifactJSON(session, fmt.Sprintf("architect_critic_revision_%d_meta.json", architectAttempt), agent.StepMeta{
		AgentID: "architect", ModelRef: e.Config.Architect.Model,
		ModelDisplay: archMeta.ModelDisplay, Provider: archMeta.Provider, ContextWindow: archMeta.ContextWindow,
		StartTime: revStart, EndTime: time.Now(),
		ClaudeSessionID: planSessionID, Status: "done",
		InputTokens: revisedUsage.Input, OutputTokens: revisedUsage.Output,
		ClaudeProjectPath:    claudeProjectPath(session),
		ClaudeSessionLogPath: archCritRevLogCopy,
		ClaudePlanFilePath:   archCritRevPlanFilePath,
	})
	logClaudeSession(logger, "architect", architectAttempt, planSessionID, archCritRevLogCopy, session)
	logAgentEvent(logger, "agent_done", "architect", architectAttempt, revisedUsage, nil, agentStart)
	emit(Event{Type: EventAgentDone, AgentID: "architect",
		InputTokens: revisedUsage.Input, OutputTokens: revisedUsage.Output})

	if revisedPlan != nil {
		planMarkdown = revisedPlan.Markdown
	}

	return &criticPhaseResult{
		PlanMarkdown:  planMarkdown,
		PlanWarnings:  agent.CheckPlanHealth(planMarkdown),
		PlanSessionID: planSessionID,
		CriticReport:  criticMarkdown,
	}, true
}

func (e *Engine) failArchitect(
	session agent.SessionDir,
	sessionID string,
	logger *slog.Logger,
	start time.Time,
	agentID string,
	attempt int,
	err error,
	_ *agent.Planner,
	cwd string,
	stream *streamCapture,
	streamOut chan<- harness.Event,
	emit func(Event),
) {
	archLogCopy, cpErr := copySessionLog(session, cwd, sessionID, "architect_session.jsonl")
	if cpErr != nil {
		logger.Warn("copy session log", "agent", agentID, "err", cpErr)
	}
	archMeta := resolveAgentMeta(e.Config, e.Config.Architect.Model)
	archPlanFilePath := ""
	if archLogCopy != "" {
		archPlanFilePath, _ = harness.ExtractPlanFilePath(archLogCopy)
	}
	writeArtifactJSON(session, "architect_meta.json", agent.StepMeta{
		AgentID: agentID, ModelRef: e.Config.Architect.Model, StartTime: start, EndTime: time.Now(),
		ModelDisplay: archMeta.ModelDisplay, Provider: archMeta.Provider, ContextWindow: archMeta.ContextWindow,
		ClaudeSessionID: sessionID, Status: "failed", Error: err.Error(),
		ClaudeProjectPath:    claudeProjectPath(session),
		ClaudeSessionLogPath: archLogCopy,
		ClaudePlanFilePath:   archPlanFilePath,
	})
	logClaudeSession(logger, agentID, attempt, sessionID, archLogCopy, session)
	logAgentEvent(logger, "agent_failed", agentID, attempt, harness.TokenUsage{}, err, nil)
	emit(Event{Type: EventAgentFailed, AgentID: agentID, Err: err})
	emit(Event{Type: EventError, Err: fmt.Errorf("planning: %w", err)})
}
