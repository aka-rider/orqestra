package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/harness"
)

// researchResult holds the output of the research phase.
type researchResult struct {
	DraftMarkdown       string
	DraftUsage          harness.TokenUsage
	ResearchSessionID   string
}

// runResearch executes the researcher agent and writes its artifacts.
// Returns the research result on success, or returns an error event and false on failure.
func (e *Engine) runResearch(
	ctx context.Context,
	session agent.SessionDir,
	emit func(Event),
	logger *slog.Logger,
	agentStart map[string]time.Time,
	stream *streamCapture,
	streamOut chan<- harness.Event,
	researchPrompt string,
) (*researchResult, bool) {
	emit(Event{Type: EventPhaseChange, Phase: PhaseResearching})
	logger.Info("phase", "phase", string(PhaseResearching))
	logAgentEvent(logger, "agent_started", "researcher", 1, harness.TokenUsage{}, nil, agentStart)
	emit(Event{Type: EventAgentStarted, AgentID: "researcher", Meta: resolveAgentMeta(e.Config, e.Config.Researcher.Model)})
	stream.SetAgent("researcher")

	researcherPlanner := agent.NewPlanner(e.Runners.Researcher, e.Config.Researcher.SystemPrompt)
	researchStart := time.Now()

	researchAttempts := e.Config.Retry.ResearcherAttempts
	if researchAttempts < 1 {
		researchAttempts = 1
	}

	var draft agent.RawPlan
	var draftUsage harness.TokenUsage
	var researchSessionID string
	var err error

	for attempt := 1; attempt <= researchAttempts; attempt++ {
		var rResult agent.PlanResult
		rResult, err = runPlanner(ctx, researcherPlanner, researchPrompt, stream, streamOut)
		if err == nil {
			draft.Markdown = rResult.Plan
			draftUsage = rResult.Usage
			researchSessionID = rResult.SessionID
			break
		}
		if attempt < researchAttempts {
			slog.Warn("researcher attempt failed, retrying", "attempt", attempt, "err", err)
			stream.SetAgent("researcher")
		}
	}
	if err != nil {
		e.failResearch(session, researchSessionID, logger, researchStart, err, emit)
		return nil, false
	}

	resMeta := resolveAgentMeta(e.Config, e.Config.Researcher.Model)
	researchLogCopy, cpErr := copySessionLog(session, "", researchSessionID, "researcher_session.jsonl")
	if cpErr != nil {
		logger.Warn("copy session log", "agent", "researcher", "err", cpErr)
	}
	writeArtifact(session, "researcher_draft.md", draft.Markdown)
	writeArtifactJSON(session, "researcher_meta.json", agent.StepMeta{
		AgentID: "researcher", ModelRef: e.Config.Researcher.Model, StartTime: researchStart, EndTime: time.Now(),
		ModelDisplay: resMeta.ModelDisplay, Provider: resMeta.Provider, ContextWindow: resMeta.ContextWindow,
		ClaudeSessionID: researchSessionID, Status: "done",
		InputTokens: draftUsage.Input, OutputTokens: draftUsage.Output,
		ClaudeProjectPath:    claudeProjectPath(session),
		ClaudeSessionLogPath: researchLogCopy,
	})
	logClaudeSession(logger, "researcher", 1, researchSessionID, researchLogCopy, session)
	logAgentEvent(logger, "agent_done", "researcher", 1, draftUsage, nil, agentStart)
	emit(Event{Type: EventAgentDone, AgentID: "researcher",
		InputTokens: draftUsage.Input, OutputTokens: draftUsage.Output,
		ResearchDraft: draft.Markdown})

	return &researchResult{
		DraftMarkdown:     draft.Markdown,
		DraftUsage:        draftUsage,
		ResearchSessionID: researchSessionID,
	}, true
}

func (e *Engine) failResearch(
	session agent.SessionDir,
	sessionID string,
	logger *slog.Logger,
	start time.Time,
	err error,
	emit func(Event),
) {
	researchLogCopy, cpErr := copySessionLog(session, "", sessionID, "researcher_session.jsonl")
	if cpErr != nil {
		logger.Warn("copy session log", "agent", "researcher", "err", cpErr)
	}
	resMeta := resolveAgentMeta(e.Config, e.Config.Researcher.Model)
	writeArtifactJSON(session, "researcher_meta.json", agent.StepMeta{
		AgentID: "researcher", ModelRef: e.Config.Researcher.Model, StartTime: start, EndTime: time.Now(),
		ModelDisplay: resMeta.ModelDisplay, Provider: resMeta.Provider, ContextWindow: resMeta.ContextWindow,
		ClaudeSessionID: sessionID, Status: "failed", Error: err.Error(),
		ClaudeProjectPath:    claudeProjectPath(session),
		ClaudeSessionLogPath: researchLogCopy,
	})
	logClaudeSession(logger, "researcher", 1, sessionID, researchLogCopy, session)
	logAgentEvent(logger, "agent_failed", "researcher", 1, harness.TokenUsage{}, err, nil)
	emit(Event{Type: EventAgentFailed, AgentID: "researcher", Err: err})
	emit(Event{Type: EventError, Err: fmt.Errorf("research: %w", err)})
}
