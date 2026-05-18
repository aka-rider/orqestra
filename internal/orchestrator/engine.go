package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/plan"
	"github.com/xiii/orqestra/internal/worktree"
)

// Input is the user's request to the orchestrator.
type Input struct {
	Prompt      string
	AutoApprove bool
	PlanFile    string // pre-loaded plan markdown (--plan flag)
	NoExecute   bool   // stop after plan gate
}

func guardPrompt(assembled, original, agentID string) string {
	out, tripped := agent.CheckPromptIntegrity(assembled, original)
	if tripped {
		slog.Warn("prompt integrity canary tripped", "agent", agentID)
	}
	return out
}

// RunStatus classifies the final outcome.
type RunStatus string

const (
	StatusSuccess RunStatus = "success"
	StatusFailed  RunStatus = "failed"
)

// Result is the final output of an orchestrator run.
type Result struct {
	Status           RunStatus
	FinalPlan        string
	WorkerValidation string
	RunDir           string
}

// RunDirFactory creates a session directory for artifact persistence.
type RunDirFactory func(slug string) (agent.SessionDir, error)

// Runners holds all runners for each agent role.
type Runners struct {
	Researcher harness.ContinuableRunner
	Architect  harness.ContinuableRunner
	Critic     harness.ContinuableRunner
	Worker     harness.ContinuableRunner
}

// Engine is the hardcoded Go orchestrator that runs the full pipeline.
type Engine struct {
	Config         *config.Config
	Runners        Runners
	RunDirFactory  RunDirFactory
	QuestionBridge *harness.QuestionBridge
	// WorktreeRunnerFactory, when set, is called just before the worker phase to
	// create a ContinuableRunner scoped to the worktree at the given path.
	// If nil, the default Runners.Worker is used with repo write access.
	WorktreeRunnerFactory func(worktreePath string) harness.ContinuableRunner
}

// RunChannels provides bidirectional communication between Engine and TUI.
type RunChannels struct {
	Events    <-chan Event
	Decisions chan<- Decision
	Stream    *StreamRing
}

// Start launches the pipeline in a goroutine. Returns channels immediately.
func (e *Engine) Start(ctx context.Context, input Input) RunChannels {
	events := make(chan Event, 16)
	decisions := make(chan Decision, 1)
	stream := NewStreamRing(200)

	go func() {
		defer close(events)
		e.run(ctx, input, events, decisions, stream)
	}()

	return RunChannels{Events: events, Decisions: decisions, Stream: stream}
}

// Run executes the full pipeline synchronously (legacy callback API).
func (e *Engine) Run(ctx context.Context, input Input, emit func(Event)) (Result, error) {
	channels := e.Start(ctx, Input{
		Prompt:      input.Prompt,
		AutoApprove: true,
	})

	var result Result
	var lastErr error
	for event := range channels.Events {
		if emit != nil {
			emit(event)
		}
		if event.Type == EventError && event.Err != nil {
			lastErr = event.Err
		}
		if event.Type == EventComplete {
			status := event.Status
			if status == "" {
				status = StatusSuccess
			}
			result = Result{
				Status:           status,
				FinalPlan:        event.FinalPlan,
				WorkerValidation: event.WorkerValidation,
				RunDir:           event.RunDir,
			}
		}
	}
	if lastErr != nil {
		return Result{Status: StatusFailed}, lastErr
	}
	return result, nil
}

func (e *Engine) run(ctx context.Context, input Input, events chan<- Event, decisions <-chan Decision, stream *StreamRing) {
	emit := func(ev Event) {
		select {
		case events <- ev:
		case <-ctx.Done():
		}
	}

	// Create run directory
	var session agent.SessionDir
	if e.RunDirFactory != nil {
		var err error
		session, err = e.RunDirFactory("run")
		if err != nil {
			emit(Event{Type: EventError, Err: fmt.Errorf("create run directory: %w", err)})
			return
		}
	}

	if session.Path != "" {
		emit(Event{Type: EventRunDirReady, RunDir: session.Path})
		writeArtifact(session, "prompt.md", input.Prompt)
	}

	// --- Run Log ---
	logger := slog.Default() // fallback: global logger (usually io.Discard in TUI mode)
	if session.Path != "" {
		logPath := filepath.Join(session.Path, "run.log")
		logFile, logErr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if logErr != nil {
			slog.Warn("could not create run log", "err", logErr)
		} else {
			logger = slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug}))
			slog.SetDefault(logger)
			defer func() {
				slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
				logFile.Close()
			}()
		}
	}
	logger.Info("run started", "prompt_len", len(input.Prompt), "auto_approve", input.AutoApprove, "plan_file", input.PlanFile != "")

	// --- Per-agent lifecycle log helpers ---
	agentStart := map[string]time.Time{}
	architectAttempt := 0
	logAgentEvent := func(event, agentID string, attempt int, usage harness.TokenUsage, err error) {
		key := agentID + ":" + strconv.Itoa(attempt)
		switch event {
		case "agent_started":
			agentStart[key] = time.Now()
			logger.Info("agent_started", "agent", agentID, "attempt", attempt)
		case "agent_done":
			var durMS int64
			if start, ok := agentStart[key]; ok {
				durMS = time.Since(start).Milliseconds()
			}
			logger.Info("agent_done", "agent", agentID, "attempt", attempt,
				"input_tokens", usage.Input, "output_tokens", usage.Output,
				"duration_ms", durMS)
		case "agent_failed":
			var durMS int64
			if start, ok := agentStart[key]; ok {
				durMS = time.Since(start).Milliseconds()
			}
			errStr := ""
			if err != nil {
				errStr = err.Error()
			}
			logger.Info("agent_failed", "agent", agentID, "attempt", attempt,
				"input_tokens", usage.Input, "output_tokens", usage.Output,
				"duration_ms", durMS, "err", errStr)
		}
	}
	logClaudeSession := func(agentID string, attempt int, sessionID, sessionLogCopy string) {
		if sessionID == "" {
			logger.Warn("claude_session_missing", "agent", agentID, "attempt", attempt)
			return
		}
		logger.Info("claude_session",
			"agent", agentID, "attempt", attempt,
			"session_id", sessionID,
			"project_path", claudeProjectPath(session),
			"session_log_copy", sessionLogCopy)
	}
	logClaudeSessionPre := func(agentID string, attempt int, sessionID string) {
		if sessionID == "" {
			logger.Warn("claude_session_missing", "agent", agentID, "attempt", attempt)
			return
		}
		logger.Info("claude_session",
			"agent", agentID, "attempt", attempt,
			"session_id", sessionID,
			"project_path", claudeProjectPath(session))
	}

	cwd := ""
	if session.Path != "" {
		cwd = filepath.Dir(filepath.Dir(filepath.Dir(session.Path)))
	}

	// --- Question Bridge ---
	if e.QuestionBridge != nil {
		if err := e.QuestionBridge.Start(ctx); err != nil {
			slog.Warn("question bridge failed to start, continuing without question support", "err", err)
		} else {
			defer e.QuestionBridge.Stop()
			go func() {
				for {
					select {
					case q, ok := <-e.QuestionBridge.Questions():
						if !ok {
							return
						}
						emit(Event{Type: EventUserQuestion, UserQuestion: q})
					case <-ctx.Done():
						return
					}
				}
			}()
		}
	}

	// --- Research ---
	researcherInput := input.Prompt
	var finalPlanMarkdown string
	var finalPlanWarnings []string
	var criticReportMarkdown string
	var planSessionID string // function scope — survives across gate loop iterations
	architectPlanner := agent.NewPlanner(e.Runners.Architect, e.Config.Architect.SystemPrompt)

	// Initialize plan version history (git micro-repo)
	var planRepo *plan.GitRepo
	if session.Path != "" {
		var repoErr error
		planRepo, repoErr = plan.NewGitRepo(session.Path)
		if repoErr != nil {
			slog.Warn("plan history unavailable — diff disabled", "err", repoErr)
		}
	}

	var lastArchitectHash string // tracks HEAD after architect commits, for diff generation

	// If a pre-loaded plan was provided, skip research and planning
	if input.PlanFile != "" {
		finalPlanMarkdown = input.PlanFile
		if planRepo != nil {
			if err := planRepo.CommitPlanAndDialog(finalPlanMarkdown, plan.DialogEntry{
				Timestamp: time.Now(), Role: "user", Message: "plan loaded from file",
			}); err != nil {
				logger.Warn("plan commit failed", "err", err)
			}
		}
		goto planGate
	}

	// --- Research ---
	{
		emit(Event{Type: EventPhaseChange, Phase: PhaseResearching})
		logger.Info("phase", "phase", string(PhaseResearching))
		logAgentEvent("agent_started", "researcher", 1, harness.TokenUsage{}, nil)
		emit(Event{Type: EventAgentStarted, AgentID: "researcher"})
		stream.SetAgent("researcher")

		researcherPlanner := agent.NewPlanner(e.Runners.Researcher, e.Config.Researcher.SystemPrompt)
		researchStart := time.Now()

		researchAttempts := e.Config.Retry.ResearcherAttempts
		if researchAttempts < 1 {
			researchAttempts = 1
		}

		researchPrompt := guardPrompt(researcherInput, input.Prompt, "researcher")

		var draft agent.RawPlan
		var draftUsage harness.TokenUsage
		var researchSessionID string
		var err error
		for attempt := 1; attempt <= researchAttempts; attempt++ {
			var rResult agent.PlanResult
			rResult, err = researcherPlanner.Run(ctx, researchPrompt, &streamWriter{ring: stream})
			if err == nil {
				draft.Markdown = rResult.Plan
				draftUsage = rResult.Usage
				researchSessionID = rResult.SessionID
				break
			}
			if attempt < researchAttempts {
				slog.Warn("researcher attempt failed, retrying", "attempt", attempt, "err", err)
				stream.SetAgent("researcher") // reset stream for retry
			}
		}
		if err != nil {
			researchLogCopy, cpErr := agent.CopySessionLog(session, cwd, researchSessionID, "researcher_session.jsonl")
			if cpErr != nil {
				logger.Warn("copy session log", "agent", "researcher", "err", cpErr)
			}
			writeArtifactJSON(session, "researcher_meta.json", agent.StepMeta{
				AgentID: "researcher", ModelRef: e.Config.Researcher.Model, StartTime: researchStart, EndTime: time.Now(),
				ClaudeSessionID: researchSessionID, Status: "failed", Error: err.Error(),
				ClaudeProjectPath:    claudeProjectPath(session),
				ClaudeSessionLogPath: researchLogCopy,
			})
			logClaudeSession("researcher", 1, researchSessionID, researchLogCopy)
			logAgentEvent("agent_failed", "researcher", 1, harness.TokenUsage{}, err)
			emit(Event{Type: EventAgentFailed, AgentID: "researcher", Err: err})
			emit(Event{Type: EventError, Err: fmt.Errorf("research: %w", err)})
			return
		}

		researchLogCopy, cpErr := agent.CopySessionLog(session, cwd, researchSessionID, "researcher_session.jsonl")
		if cpErr != nil {
			logger.Warn("copy session log", "agent", "researcher", "err", cpErr)
		}
		writeArtifact(session, "researcher_draft.md", draft.Markdown)
		writeArtifactJSON(session, "researcher_meta.json", agent.StepMeta{
			AgentID: "researcher", ModelRef: e.Config.Researcher.Model, StartTime: researchStart, EndTime: time.Now(),
			ClaudeSessionID: researchSessionID, Status: "done",
			InputTokens: draftUsage.Input, OutputTokens: draftUsage.Output,
			ClaudeProjectPath:    claudeProjectPath(session),
			ClaudeSessionLogPath: researchLogCopy,
		})
		logClaudeSession("researcher", 1, researchSessionID, researchLogCopy)
		logAgentEvent("agent_done", "researcher", 1, draftUsage, nil)
		emit(Event{Type: EventAgentDone, AgentID: "researcher",
			InputTokens: draftUsage.Input, OutputTokens: draftUsage.Output,
			ResearchDraft: draft.Markdown})

		// --- Planning ---
		emit(Event{Type: EventPhaseChange, Phase: PhasePlanning})
		logger.Info("phase", "phase", string(PhasePlanning))
		architectAttempt++
		logAgentEvent("agent_started", "architect", architectAttempt, harness.TokenUsage{}, nil)
		emit(Event{Type: EventAgentStarted, AgentID: "architect"})
		stream.SetAgent("architect")

		planStart := time.Now()

		architectAttempts := e.Config.Retry.ArchitectAttempts
		if architectAttempts < 1 {
			architectAttempts = 1
		}

		architectPrompt := guardPrompt(agent.ArchitectPrompt(input.Prompt, draft.Markdown), input.Prompt, "architect")

		var planResult agent.RawPlan
		var planUsage harness.TokenUsage
		var planErr error
		for attempt := 1; attempt <= architectAttempts; attempt++ {
			var aResult agent.PlanResult
			aResult, planErr = architectPlanner.Run(ctx, architectPrompt, &streamWriter{ring: stream})
			if planErr == nil {
				planSessionID = aResult.SessionID
				planUsage = aResult.Usage
				planResult = agent.RawPlan{Markdown: aResult.Plan, Warnings: agent.CheckPlanHealth(aResult.Plan)}
				break
			}
			if attempt < architectAttempts {
				slog.Warn("architect attempt failed, retrying", "attempt", attempt, "err", planErr)
				stream.SetAgent("architect") // reset stream for retry
			}
		}
		if planErr != nil {
			archLogCopy, cpErr := agent.CopySessionLog(session, cwd, planSessionID, "architect_session.jsonl")
			if cpErr != nil {
				logger.Warn("copy session log", "agent", "architect", "err", cpErr)
			}
			archPlanFilePath := ""
			if archLogCopy != "" {
				archPlanFilePath, _ = harness.ExtractPlanFilePath(archLogCopy) // best-effort
			}
			writeArtifactJSON(session, "architect_meta.json", agent.StepMeta{
				AgentID: "architect", ModelRef: e.Config.Architect.Model, StartTime: planStart, EndTime: time.Now(),
				ClaudeSessionID: planSessionID, Status: "failed", Error: planErr.Error(),
				ClaudeProjectPath:    claudeProjectPath(session),
				ClaudeSessionLogPath: archLogCopy,
				ClaudePlanFilePath:   archPlanFilePath,
			})
			logClaudeSession("architect", architectAttempt, planSessionID, archLogCopy)
			logAgentEvent("agent_failed", "architect", architectAttempt, harness.TokenUsage{}, planErr)
			emit(Event{Type: EventAgentFailed, AgentID: "architect", Err: planErr})
			emit(Event{Type: EventError, Err: fmt.Errorf("planning: %w", planErr)})
			return
		}

		archLogCopy, cpErr := agent.CopySessionLog(session, cwd, planSessionID, "architect_session.jsonl")
		if cpErr != nil {
			logger.Warn("copy session log", "agent", "architect", "err", cpErr)
		}
		archPlanFilePath := ""
		if archLogCopy != "" {
			archPlanFilePath, _ = harness.ExtractPlanFilePath(archLogCopy) // best-effort
		}
		writeArtifactJSON(session, "architect_meta.json", agent.StepMeta{
			AgentID: "architect", ModelRef: e.Config.Architect.Model, StartTime: planStart, EndTime: time.Now(),
			ClaudeSessionID: planSessionID, Status: "done",
			InputTokens: planUsage.Input, OutputTokens: planUsage.Output,
			ClaudeProjectPath:    claudeProjectPath(session),
			ClaudeSessionLogPath: archLogCopy,
			ClaudePlanFilePath:   archPlanFilePath,
		})
		logClaudeSession("architect", architectAttempt, planSessionID, archLogCopy)
		logAgentEvent("agent_done", "architect", architectAttempt, planUsage, nil)
		emit(Event{Type: EventAgentDone, AgentID: "architect",
			InputTokens: planUsage.Input, OutputTokens: planUsage.Output})

		finalPlanWarnings = planResult.Warnings
		finalPlanMarkdown = planResult.Markdown
		if planRepo != nil {
			if err := planRepo.CommitPlanAndDialog(finalPlanMarkdown, plan.DialogEntry{
				Timestamp: time.Now(), Role: "architect", Message: "initial plan",
				OutputTokens: int(planUsage.Output),
			}); err != nil {
				logger.Warn("plan commit failed", "err", err)
			}
			if hash, hashErr := planRepo.HeadCommitHash(); hashErr == nil {
				lastArchitectHash = hash
			}
		}
	}

	// --- Critic Review ---
	if e.Runners.Critic != nil {
		emit(Event{Type: EventPhaseChange, Phase: PhaseCritiquing})
		logger.Info("phase", "phase", string(PhaseCritiquing))
		logAgentEvent("agent_started", "critic", 1, harness.TokenUsage{}, nil)
		emit(Event{Type: EventAgentStarted, AgentID: "critic"})
		stream.SetAgent("critic")

		criticPlanner := agent.NewPlanner(e.Runners.Critic, e.Config.Critic.SystemPrompt)
		criticStart := time.Now()

		criticAttempts := e.Config.Retry.CriticAttempts
		if criticAttempts < 1 {
			criticAttempts = 1
		}

		criticPrompt := guardPrompt(agent.CriticReviewPrompt(input.Prompt, finalPlanMarkdown), input.Prompt, "critic")

		var criticMarkdown string
		var criticUsage harness.TokenUsage
		var criticSessionID string
		var criticErr error
		for attempt := 1; attempt <= criticAttempts; attempt++ {
			var cResult agent.PlanResult
			cResult, criticErr = criticPlanner.Run(ctx, criticPrompt, &streamWriter{ring: stream})
			if criticErr == nil {
				criticMarkdown = cResult.Plan
				criticUsage = cResult.Usage
				criticSessionID = cResult.SessionID
				break
			}
			if attempt < criticAttempts {
				slog.Warn("critic attempt failed, retrying", "attempt", attempt, "err", criticErr)
				stream.SetAgent("critic")
			}
		}
		if criticErr != nil {
			criticLogCopy, cpErr := agent.CopySessionLog(session, cwd, criticSessionID, "critic_session.jsonl")
			if cpErr != nil {
				logger.Warn("copy session log", "agent", "critic", "err", cpErr)
			}
			writeArtifactJSON(session, "critic_meta.json", agent.StepMeta{
				AgentID: "critic", ModelRef: e.Config.Critic.Model, StartTime: criticStart, EndTime: time.Now(),
				ClaudeSessionID: criticSessionID, Status: "failed", Error: criticErr.Error(),
				ClaudeProjectPath:    claudeProjectPath(session),
				ClaudeSessionLogPath: criticLogCopy,
			})
			logClaudeSession("critic", 1, criticSessionID, criticLogCopy)
			logAgentEvent("agent_failed", "critic", 1, harness.TokenUsage{}, criticErr)
			emit(Event{Type: EventAgentFailed, AgentID: "critic", Err: criticErr})
			emit(Event{Type: EventError, Err: fmt.Errorf("critic review: %w", criticErr)})
			return
		}

		criticLogCopy, cpErr := agent.CopySessionLog(session, cwd, criticSessionID, "critic_session.jsonl")
		if cpErr != nil {
			logger.Warn("copy session log", "agent", "critic", "err", cpErr)
		}
		writeArtifact(session, "critic_report.md", criticMarkdown)
		writeArtifactJSON(session, "critic_meta.json", agent.StepMeta{
			AgentID: "critic", ModelRef: e.Config.Critic.Model, StartTime: criticStart, EndTime: time.Now(),
			ClaudeSessionID: criticSessionID, Status: "done",
			InputTokens: criticUsage.Input, OutputTokens: criticUsage.Output,
			ClaudeProjectPath:    claudeProjectPath(session),
			ClaudeSessionLogPath: criticLogCopy,
		})
		logClaudeSession("critic", 1, criticSessionID, criticLogCopy)
		logAgentEvent("agent_done", "critic", 1, criticUsage, nil)
		emit(Event{Type: EventAgentDone, AgentID: "critic",
			InputTokens: criticUsage.Input, OutputTokens: criticUsage.Output})

		criticReportMarkdown = criticMarkdown

		// Persist critic report in dialog
		if planRepo != nil {
			firstLine := criticMarkdown
			if idx := strings.IndexByte(firstLine, '\n'); idx > 0 {
				firstLine = firstLine[:idx]
			}
			if len(firstLine) > 50 {
				firstLine = firstLine[:50]
			}
			if err := planRepo.CommitDialog(plan.DialogEntry{
				Timestamp: time.Now(), Role: "critic", Message: firstLine,
				OutputTokens: int(criticUsage.Output),
			}); err != nil {
				logger.Warn("critic dialog commit failed", "err", err)
			}
		}

		// --- Architect Second Pass (critic feedback) ---
		architectAttempt++
		logAgentEvent("agent_started", "architect", architectAttempt, harness.TokenUsage{}, nil)
		emit(Event{Type: EventAgentStarted, AgentID: "architect"})
		stream.SetAgent("architect")
		revStart := time.Now()

		critBaseline, critBaselineErr := agent.ReadPlanFromRun(harness.RunResult{SessionID: planSessionID})
		if critBaselineErr != nil {
			slog.Debug("could not snapshot plan file baseline before critic revision", "err", critBaselineErr)
		}
		critRevResult, revErr := architectPlanner.Continue(ctx, planSessionID, agent.CriticContinuePrompt(finalPlanMarkdown, criticMarkdown), &streamWriter{ring: stream})
		chatResponse := critRevResult.Chat
		revisedPlan := agent.DetectPlanRevision(critRevResult.Plan, critBaseline, critBaselineErr, finalPlanMarkdown)
		revisedUsage := critRevResult.Usage
		_ = chatResponse // architect may include reasoning alongside plan revision

		if revErr != nil {
			archCritRevLogCopy, cpErr := agent.CopySessionLog(session, cwd, planSessionID, "architect_critic_revision_session.jsonl")
			if cpErr != nil {
				logger.Warn("copy session log", "agent", "architect", "err", cpErr)
			}
			archCritRevPlanFilePath := ""
			if archCritRevLogCopy != "" {
				archCritRevPlanFilePath, _ = harness.ExtractPlanFilePath(archCritRevLogCopy) // best-effort
			}
			writeArtifactJSON(session, "architect_critic_revision_meta.json", agent.StepMeta{
				AgentID: "architect", ModelRef: e.Config.Architect.Model,
				StartTime: revStart, EndTime: time.Now(),
				ClaudeSessionID: planSessionID, Status: "failed", Error: revErr.Error(),
				ClaudeProjectPath:    claudeProjectPath(session),
				ClaudeSessionLogPath: archCritRevLogCopy,
				ClaudePlanFilePath:   archCritRevPlanFilePath,
			})
			logClaudeSession("architect", architectAttempt, planSessionID, archCritRevLogCopy)
			logAgentEvent("agent_failed", "architect", architectAttempt, harness.TokenUsage{}, revErr)
			emit(Event{Type: EventAgentFailed, AgentID: "architect", Err: revErr})
			emit(Event{Type: EventError, Err: fmt.Errorf("architect critic revision: %w", revErr)})
			return
		}

		archCritRevLogCopy, cpErr := agent.CopySessionLog(session, cwd, planSessionID, "architect_critic_revision_session.jsonl")
		if cpErr != nil {
			logger.Warn("copy session log", "agent", "architect", "err", cpErr)
		}
		archCritRevPlanFilePath := ""
		if archCritRevLogCopy != "" {
			archCritRevPlanFilePath, _ = harness.ExtractPlanFilePath(archCritRevLogCopy) // best-effort
		}
		writeArtifactJSON(session, "architect_critic_revision_meta.json", agent.StepMeta{
			AgentID: "architect", ModelRef: e.Config.Architect.Model,
			StartTime: revStart, EndTime: time.Now(),
			ClaudeSessionID: planSessionID, Status: "done",
			InputTokens: revisedUsage.Input, OutputTokens: revisedUsage.Output,
			ClaudeProjectPath:    claudeProjectPath(session),
			ClaudeSessionLogPath: archCritRevLogCopy,
			ClaudePlanFilePath:   archCritRevPlanFilePath,
		})
		logClaudeSession("architect", architectAttempt, planSessionID, archCritRevLogCopy)
		logAgentEvent("agent_done", "architect", architectAttempt, revisedUsage, nil)
		emit(Event{Type: EventAgentDone, AgentID: "architect",
			InputTokens: revisedUsage.Input, OutputTokens: revisedUsage.Output})

		if revisedPlan != nil {
			finalPlanMarkdown = revisedPlan.Markdown
			finalPlanWarnings = revisedPlan.Warnings
			if planRepo != nil {
				if commitErr := planRepo.CommitPlanAndDialog(finalPlanMarkdown, plan.DialogEntry{
					Timestamp: time.Now(), Role: "architect", Message: "Re: critic feedback",
					OutputTokens: int(revisedUsage.Output),
				}); commitErr != nil {
					logger.Warn("plan commit failed", "err", commitErr)
				}
				if hash, hashErr := planRepo.HeadCommitHash(); hashErr == nil {
					lastArchitectHash = hash
				}
			}
		} else {
			// No plan change, but still log the dialog turn
			if planRepo != nil {
				if commitErr := planRepo.CommitDialog(plan.DialogEntry{
					Timestamp: time.Now(), Role: "architect", Message: "Re: critic feedback (no changes)",
					OutputTokens: int(revisedUsage.Output),
				}); commitErr != nil {
					logger.Warn("dialog commit failed", "err", commitErr)
				}
			}
		}
	}

planGate:
	// --- Plan Approval Gate ---
	emit(Event{Type: EventPlanReady, FinalPlan: finalPlanMarkdown})
	logger.Info("plan_ready", "len", len(finalPlanMarkdown), "warnings", len(finalPlanWarnings))

	if !input.AutoApprove {
		for {
			var planDiff string
			if planRepo != nil {
				diff, diffErr := planRepo.Diff()
				if diffErr != nil {
					slog.Warn("plan diff failed", "err", diffErr)
				}
				planDiff = diff
			}
			writeArtifact(session, "final_plan.md", finalPlanMarkdown)
			var historyDir, historyHeadSHA string
			if planRepo != nil {
				historyDir = planRepo.Dir()
				if sha, headErr := planRepo.HeadCommitHash(); headErr == nil {
					historyHeadSHA = sha
				} else {
					logger.Warn("plan head sha lookup failed", "err", headErr)
				}
			}
			emit(Event{Type: EventGateRequest, Gate: GateRequest{
				Type:               GatePlanApproval,
				FinalPlanMarkdown:  finalPlanMarkdown,
				PlanFilePath:       session.ArtifactPath("final_plan.md"),
				PlanDiff:           planDiff,
				PlanWarnings:       finalPlanWarnings,
				CriticReport:       criticReportMarkdown,
				PlanHistoryDir:     historyDir,
				PlanHistoryHeadSHA: historyHeadSHA,
			}})
			logger.Info("gate_request", "auto_approve", false)

			select {
			case decision := <-decisions:
				switch decision.Type {
				case DecisionCancel:
					emit(Event{Type: EventComplete, Phase: PhaseDone})
					logger.Info("run_complete", "status", "cancelled")
					return
				// NOTE: Revert flow (TUI plan-history viewer, Ctrl+Y) reuses this
				// branch with Comment == "" and an EditedContent payload taken from
				// a historical commit. The Comment-empty guard below MUST keep
				// skipping architect re-engagement for revert to remain
				// non-destructive (the architect would otherwise overwrite the
				// user's chosen revision on the next iteration). Tests:
				// TestGate_DecisionEditEmptyComment_NoArchitect.
				case DecisionEdit:
					edited := strings.TrimSpace(decision.EditedContent)
					finalPlanMarkdown = edited
					finalPlanWarnings = agent.CheckPlanHealth(edited)
					logger.Info("gate: user edit", "comment_len", len(decision.Comment))
					logger.Info("gate_decision", "decision", "edit", "comment_len", len(decision.Comment))
					if planRepo != nil {
						if err := planRepo.CommitPlan(edited, "user: manual edit"); err != nil {
							logger.Warn("plan commit failed", "err", err)
						}
					}
					// If user provided a comment via the confirmation dialog,
					// send edit + diff + comment to architect in one shot.
					// Empty comment is the revert path: skip architect re-engagement.
					if decision.Comment != "" && planSessionID != "" {
						var diff string
						if planRepo != nil && lastArchitectHash != "" {
							diff, _ = planRepo.DiffPlain(lastArchitectHash)
						}
						if planRepo != nil {
							if err := planRepo.CommitDialog(plan.DialogEntry{
								Timestamp: time.Now(), Role: "user", Message: decision.Comment,
							}); err != nil {
								logger.Warn("user comment dialog commit failed", "err", err)
							}
						}
						architectAttempt++
						logAgentEvent("agent_started", "architect", architectAttempt, harness.TokenUsage{}, nil)
						emit(Event{Type: EventAgentStarted, AgentID: "architect"})
						stream.SetAgent("architect")
						revStart := time.Now()

						editBaseline, editBaselineErr := agent.ReadPlanFromRun(harness.RunResult{SessionID: planSessionID})
						if editBaselineErr != nil {
							slog.Debug("could not snapshot plan file baseline before edit revision", "err", editBaselineErr)
						}
						var editContinuePrompt string
						if diff != "" {
							editContinuePrompt = agent.ContinueWithDiffPrompt(finalPlanMarkdown, diff, decision.Comment)
						} else {
							editContinuePrompt = agent.ContinuePrompt(finalPlanMarkdown, decision.Comment)
						}
						editResult, err := architectPlanner.Continue(ctx, planSessionID, editContinuePrompt, &streamWriter{ring: stream})
						chatResponse := editResult.Chat
						revisedPlan := agent.DetectPlanRevision(editResult.Plan, editBaseline, editBaselineErr, finalPlanMarkdown)
						revisedUsage := editResult.Usage

						if err != nil {
							archRevLogCopy, cpErr := agent.CopySessionLog(session, cwd, planSessionID, "architect_revision_session.jsonl")
							if cpErr != nil {
								logger.Warn("copy session log", "agent", "architect", "err", cpErr)
							}
							archRevPlanFilePath := ""
							if archRevLogCopy != "" {
								archRevPlanFilePath, _ = harness.ExtractPlanFilePath(archRevLogCopy)
							}
							writeArtifactJSON(session, "architect_revision_meta.json", agent.StepMeta{
								AgentID: "architect", ModelRef: e.Config.Architect.Model,
								StartTime: revStart, EndTime: time.Now(),
								ClaudeSessionID: planSessionID, Status: "failed", Error: err.Error(),
								ClaudeProjectPath:    claudeProjectPath(session),
								ClaudeSessionLogPath: archRevLogCopy,
								ClaudePlanFilePath:   archRevPlanFilePath,
							})
							logClaudeSession("architect", architectAttempt, planSessionID, archRevLogCopy)
							logAgentEvent("agent_failed", "architect", architectAttempt, harness.TokenUsage{}, err)
							emit(Event{Type: EventAgentFailed, AgentID: "architect", Err: err})
							emit(Event{Type: EventError, Err: fmt.Errorf("architect revision: %w", err)})
							return
						}

						archRevLogCopy, cpErr := agent.CopySessionLog(session, cwd, planSessionID, "architect_revision_session.jsonl")
						if cpErr != nil {
							logger.Warn("copy session log", "agent", "architect", "err", cpErr)
						}
						archRevPlanFilePath := ""
						if archRevLogCopy != "" {
							archRevPlanFilePath, _ = harness.ExtractPlanFilePath(archRevLogCopy)
						}
						writeArtifactJSON(session, "architect_revision_meta.json", agent.StepMeta{
							AgentID: "architect", ModelRef: e.Config.Architect.Model,
							StartTime: revStart, EndTime: time.Now(),
							ClaudeSessionID: planSessionID, Status: "done",
							InputTokens: revisedUsage.Input, OutputTokens: revisedUsage.Output,
							ClaudeProjectPath:    claudeProjectPath(session),
							ClaudeSessionLogPath: archRevLogCopy,
							ClaudePlanFilePath:   archRevPlanFilePath,
						})
						logClaudeSession("architect", architectAttempt, planSessionID, archRevLogCopy)
						logAgentEvent("agent_done", "architect", architectAttempt, revisedUsage, nil)
						emit(Event{Type: EventAgentDone, AgentID: "architect",
							InputTokens: revisedUsage.Input, OutputTokens: revisedUsage.Output})

						if revisedPlan != nil {
							finalPlanMarkdown = revisedPlan.Markdown
							finalPlanWarnings = revisedPlan.Warnings
							if planRepo != nil {
								if commitErr := planRepo.CommitPlanAndDialog(finalPlanMarkdown, plan.DialogEntry{
									Timestamp: time.Now(), Role: "architect", Message: "Re: " + truncateMsg(decision.Comment, 50),
									OutputTokens: int(revisedUsage.Output),
								}); commitErr != nil {
									logger.Warn("plan commit failed", "err", commitErr)
								}
								if hash, hashErr := planRepo.HeadCommitHash(); hashErr == nil {
									lastArchitectHash = hash
								}
							}
						} else if chatResponse != "" {
							if planRepo != nil {
								if commitErr := planRepo.CommitDialog(plan.DialogEntry{
									Timestamp: time.Now(), Role: "architect",
									Message:      "Re: " + truncateMsg(decision.Comment, 50) + " (chat only)",
									OutputTokens: int(revisedUsage.Output),
								}); commitErr != nil {
									logger.Warn("dialog commit failed", "err", commitErr)
								}
							}
							emit(Event{Type: EventChatResponse, ChatText: chatResponse})
						}
					}
					// Auto-approve path: user already confirmed the edited content at the
					// TUI (^E -> save -> Yes, no comment). Skip the re-show and fall
					// through to the post-gate break, matching DecisionApprove semantics.
					// Revert path (RevertPlanIntent) and any edit-with-comment continue
					// to re-show the gate so the user reviews the revision.
					if decision.Comment != "" || !decision.AutoApprove {
						continue
					}
					logger.Info("gate: user confirmed external edit")
					logger.Info("gate_decision", "decision", "edit-auto-approve")
					// fall through to break after the select
				case DecisionComment:
					architectAttempt++
					logAgentEvent("agent_started", "architect", architectAttempt, harness.TokenUsage{}, nil)
					emit(Event{Type: EventAgentStarted, AgentID: "architect"})
					stream.SetAgent("architect")
					revStart := time.Now()
					logger.Info("gate: user comment", "comment_len", len(decision.Comment))
					logger.Info("gate_decision", "decision", "comment", "comment_len", len(decision.Comment))

					// Persist user comment in dialog
					if planRepo != nil {
						if err := planRepo.CommitDialog(plan.DialogEntry{
							Timestamp: time.Now(), Role: "user", Message: decision.Comment,
						}); err != nil {
							logger.Warn("user comment dialog commit failed", "err", err)
						}
					}

					var chatResponse string
					var revisedPlan *agent.RawPlan
					var revisedUsage harness.TokenUsage
					var err error

					if planSessionID != "" {
						commentBaseline, commentBaselineErr := agent.ReadPlanFromRun(harness.RunResult{SessionID: planSessionID})
						if commentBaselineErr != nil {
							slog.Debug("could not snapshot plan file baseline before comment revision", "err", commentBaselineErr)
						}
						var commentResult agent.PlanResult
						commentResult, err = architectPlanner.Continue(ctx, planSessionID, agent.ContinuePrompt(finalPlanMarkdown, decision.Comment), &streamWriter{ring: stream})
						if err == nil {
							chatResponse = commentResult.Chat
							revisedPlan = agent.DetectPlanRevision(commentResult.Plan, commentBaseline, commentBaselineErr, finalPlanMarkdown)
							revisedUsage = commentResult.Usage
						}
					} else {
						// Fallback for cold start (--plan flag) — no session to resume
						coldPrompt := guardPrompt(agent.ArchitectRevisionPrompt(finalPlanMarkdown, decision.Comment), input.Prompt, "architect (cold-start)")
						var coldResult agent.PlanResult
						coldResult, err = architectPlanner.Run(ctx, coldPrompt, &streamWriter{ring: stream})
						revisedUsage = coldResult.Usage // populated even on error if partial
						if err == nil {
							planSessionID = coldResult.SessionID
							revisedPlan = &agent.RawPlan{Markdown: coldResult.Plan, Warnings: agent.CheckPlanHealth(coldResult.Plan)}
						}
						chatResponse = ""
					}

					if err != nil {
						archRevLogCopy, cpErr := agent.CopySessionLog(session, cwd, planSessionID, "architect_revision_session.jsonl")
						if cpErr != nil {
							logger.Warn("copy session log", "agent", "architect", "err", cpErr)
						}
						archRevPlanFilePath := ""
						if archRevLogCopy != "" {
							archRevPlanFilePath, _ = harness.ExtractPlanFilePath(archRevLogCopy) // best-effort
						}
						writeArtifactJSON(session, "architect_revision_meta.json", agent.StepMeta{
							AgentID: "architect", ModelRef: e.Config.Architect.Model,
							StartTime: revStart, EndTime: time.Now(),
							ClaudeSessionID: planSessionID, Status: "failed", Error: err.Error(),
							ClaudeProjectPath:    claudeProjectPath(session),
							ClaudeSessionLogPath: archRevLogCopy,
							ClaudePlanFilePath:   archRevPlanFilePath,
						})
						logClaudeSession("architect", architectAttempt, planSessionID, archRevLogCopy)
						logAgentEvent("agent_failed", "architect", architectAttempt, harness.TokenUsage{}, err)
						emit(Event{Type: EventAgentFailed, AgentID: "architect", Err: err})
						emit(Event{Type: EventError, Err: fmt.Errorf("architect revision: %w", err)})
						return
					}

					archRevLogCopy, cpErr := agent.CopySessionLog(session, cwd, planSessionID, "architect_revision_session.jsonl")
					if cpErr != nil {
						logger.Warn("copy session log", "agent", "architect", "err", cpErr)
					}
					archRevPlanFilePath := ""
					if archRevLogCopy != "" {
						archRevPlanFilePath, _ = harness.ExtractPlanFilePath(archRevLogCopy) // best-effort
					}
					writeArtifactJSON(session, "architect_revision_meta.json", agent.StepMeta{
						AgentID: "architect", ModelRef: e.Config.Architect.Model,
						StartTime: revStart, EndTime: time.Now(),
						ClaudeSessionID: planSessionID, Status: "done",
						InputTokens: revisedUsage.Input, OutputTokens: revisedUsage.Output,
						ClaudeProjectPath:    claudeProjectPath(session),
						ClaudeSessionLogPath: archRevLogCopy,
						ClaudePlanFilePath:   archRevPlanFilePath,
					})
					logClaudeSession("architect", architectAttempt, planSessionID, archRevLogCopy)
					logAgentEvent("agent_done", "architect", architectAttempt, revisedUsage, nil)
					emit(Event{Type: EventAgentDone, AgentID: "architect",
						InputTokens: revisedUsage.Input, OutputTokens: revisedUsage.Output})

					if revisedPlan != nil {
						finalPlanMarkdown = revisedPlan.Markdown
						finalPlanWarnings = revisedPlan.Warnings
						if planRepo != nil {
							if commitErr := planRepo.CommitPlanAndDialog(finalPlanMarkdown, plan.DialogEntry{
								Timestamp: time.Now(), Role: "architect", Message: "Re: " + truncateMsg(decision.Comment, 50),
								OutputTokens: int(revisedUsage.Output),
							}); commitErr != nil {
								logger.Warn("plan commit failed", "err", commitErr)
							}
							if hash, hashErr := planRepo.HeadCommitHash(); hashErr == nil {
								lastArchitectHash = hash
							}
						}
					} else if chatResponse != "" {
						// Chat-only response — no plan change
						if planRepo != nil {
							if commitErr := planRepo.CommitDialog(plan.DialogEntry{
								Timestamp: time.Now(), Role: "architect",
								Message:      "Re: " + truncateMsg(decision.Comment, 50) + " (chat only)",
								OutputTokens: int(revisedUsage.Output),
							}); commitErr != nil {
								logger.Warn("dialog commit failed", "err", commitErr)
							}
						}
						emit(Event{Type: EventChatResponse, ChatText: chatResponse})
					}
					continue // re-show gate with (possibly revised) plan
				case DecisionApprove:
					logger.Info("gate: user approved plan")
					logger.Info("gate_decision", "decision", "approve")
					// proceed
				}
			case <-ctx.Done():
				return
			}
			break
		}
	}

	// Save approved plan
	writeArtifact(session, "final_plan.md", finalPlanMarkdown)

	// --- NoExecute: stop after plan gate ---
	if input.NoExecute {
		emit(Event{Type: EventPhaseChange, Phase: PhaseDone})
		logger.Info("phase", "phase", string(PhaseDone))
		emit(Event{Type: EventComplete, Phase: PhaseDone,
			FinalPlan: finalPlanMarkdown,
			Status:    StatusSuccess,
			RunDir:    session.Path,
		})
		logger.Info("run_complete", "status", "noexecute")
		return
	}

	// --- Worker Execution ---
	emit(Event{Type: EventPhaseChange, Phase: PhaseExecuting})
	logger.Info("phase", "phase", string(PhaseExecuting))
	logAgentEvent("agent_started", "worker", 1, harness.TokenUsage{}, nil)
	emit(Event{Type: EventAgentStarted, AgentID: "worker"})
	stream.SetAgent("worker")
	workerStart := time.Now()

	// Determine which branch to merge back into after the run.
	repoPath, _ := os.Getwd()
	targetBranch, branchErr := worktree.CurrentBranch(ctx, repoPath)
	if branchErr != nil {
		slog.Warn("cannot determine current branch — worktree isolation disabled", "err", branchErr)
	}

	// Create an isolated git worktree for the worker when possible.
	var wt worktree.Worktree
	var wtErr error
	workerRunner := e.Runners.Worker

	runID := ""
	if session.Path != "" && targetBranch != "" && e.WorktreeRunnerFactory != nil {
		runID = fmt.Sprintf("%d", workerStart.UnixMilli())
		wt, wtErr = worktree.Create(ctx, repoPath, session.Path, runID)
		if wtErr != nil {
			slog.Warn("worktree creation failed — falling back to writable repo", "err", wtErr)
			wt = worktree.Worktree{} // zero value = no worktree
		} else {
			workerRunner = e.WorktreeRunnerFactory(wt.Path)
		}
	}

	execPrompt := agent.BuildExecutionPromptFromPlan(finalPlanMarkdown)
	workResult, execErr := workerRunner.RunStreaming(ctx, execPrompt, "", &streamWriter{ring: stream})

	// Clean up worktree on failure or cancellation.
	if execErr != nil {
		if wt.Path != "" {
			if rmErr := wt.Remove(context.Background(), true); rmErr != nil {
				slog.Warn("worktree cleanup failed", "err", rmErr)
			}
		}
		writeArtifactJSON(session, "worker_meta.json", agent.StepMeta{
			AgentID: "worker", ModelRef: e.Config.Worker.Model, StartTime: workerStart, EndTime: time.Now(),
			Status: "failed", Error: execErr.Error(),
			ClaudeProjectPath: claudeProjectPath(session),
		})
		if workResult.SessionID == "" {
			logClaudeSessionPre("worker", 1, "")
		} else {
			logClaudeSessionPre("worker", 1, workResult.SessionID)
		}
		logAgentEvent("agent_failed", "worker", 1, harness.TokenUsage{}, execErr)
		emit(Event{Type: EventAgentFailed, AgentID: "worker", Err: execErr})
		emit(Event{Type: EventError, Err: execErr})
		return
	}

	workerLogCopy, cpErr := agent.CopySessionLog(session, cwd, workResult.SessionID, "worker_session.jsonl")
	if cpErr != nil {
		logger.Warn("copy session log", "agent", "worker", "err", cpErr)
	}
	writeArtifact(session, "worker_output.txt", workResult.Output)
	writeArtifactJSON(session, "worker_meta.json", agent.StepMeta{
		AgentID: "worker", ModelRef: e.Config.Worker.Model, StartTime: workerStart, EndTime: time.Now(),
		ClaudeSessionID: workResult.SessionID, Status: "done",
		InputTokens: workResult.Usage.Input, OutputTokens: workResult.Usage.Output,
		ClaudeProjectPath:    claudeProjectPath(session),
		ClaudeSessionLogPath: workerLogCopy,
	})
	logClaudeSession("worker", 1, workResult.SessionID, workerLogCopy)
	logAgentEvent("agent_done", "worker", 1, workResult.Usage, nil)
	emit(Event{Type: EventAgentDone, AgentID: "worker", WorkOutput: workResult.Output,
		InputTokens: workResult.Usage.Input, OutputTokens: workResult.Usage.Output})

	// lastSessionID tracks the most recent session continuation — used for
	// commit message generation after validation completes.
	lastSessionID := workResult.SessionID

	// --- Worker Self-Validation via Session Continuation ---
	emit(Event{Type: EventPhaseChange, Phase: PhaseSelfValidating})
	logger.Info("phase", "phase", string(PhaseSelfValidating))
	logAgentEvent("agent_started", "validator", 1, harness.TokenUsage{}, nil)
	emit(Event{Type: EventAgentStarted, AgentID: "validator"})
	stream.SetAgent("validator")
	valStart := time.Now()

	var validationOutput string
	retryBudget := e.Config.Retry.WorkerValidationRetries
	if retryBudget < 1 {
		retryBudget = 1
	}

	validationPrompt := agent.WorkerValidationPrompt(retryBudget)

	if workResult.SessionID != "" {
		valResult, valErr := workerRunner.RunContinue(ctx, workResult.SessionID, validationPrompt, &streamWriter{ring: stream})
		if valErr != nil {
			slog.Warn("worker self-validation failed", "err", valErr)
			valLogCopy, cpErr := agent.CopySessionLog(session, cwd, workResult.SessionID, "validator_session.jsonl")
			if cpErr != nil {
				logger.Warn("copy session log", "agent", "validator", "err", cpErr)
			}
			writeArtifactJSON(session, "validator_meta.json", agent.StepMeta{
				AgentID: "validator", ModelRef: e.Config.Worker.Model, StartTime: valStart, EndTime: time.Now(),
				ClaudeSessionID: workResult.SessionID, Status: "failed", Error: valErr.Error(),
				ClaudeProjectPath:    claudeProjectPath(session),
				ClaudeSessionLogPath: valLogCopy,
			})
			logClaudeSession("validator", 1, workResult.SessionID, valLogCopy)
			logAgentEvent("agent_failed", "validator", 1, harness.TokenUsage{}, valErr)
			emit(Event{Type: EventAgentFailed, AgentID: "validator", Err: valErr})
			// Non-fatal: proceed with whatever output we have
		} else {
			validationOutput = valResult.Output
			if valResult.SessionID != "" {
				lastSessionID = valResult.SessionID
			}
			valLogCopy, cpErr := agent.CopySessionLog(session, cwd, valResult.SessionID, "validator_session.jsonl")
			if cpErr != nil {
				logger.Warn("copy session log", "agent", "validator", "err", cpErr)
			}
			writeArtifactJSON(session, "validator_meta.json", agent.StepMeta{
				AgentID: "validator", ModelRef: e.Config.Worker.Model, StartTime: valStart, EndTime: time.Now(),
				ClaudeSessionID: valResult.SessionID, Status: "done",
				InputTokens: valResult.Usage.Input, OutputTokens: valResult.Usage.Output,
				ClaudeProjectPath:    claudeProjectPath(session),
				ClaudeSessionLogPath: valLogCopy,
			})
			logClaudeSession("validator", 1, valResult.SessionID, valLogCopy)
			logAgentEvent("agent_done", "validator", 1, valResult.Usage, nil)
			emit(Event{Type: EventAgentDone, AgentID: "validator",
				InputTokens: valResult.Usage.Input, OutputTokens: valResult.Usage.Output})
		}
	} else {
		logger.Warn("validator session missing, running disconnected")
		// Fallback: run validation as a new session (less effective but still useful)
		valResult, valErr := workerRunner.RunStreaming(ctx, validationPrompt, "", &streamWriter{ring: stream})
		if valErr != nil {
			slog.Warn("disconnected validation failed", "err", valErr)
			writeArtifactJSON(session, "validator_meta.json", agent.StepMeta{
				AgentID: "validator", ModelRef: e.Config.Worker.Model, StartTime: valStart, EndTime: time.Now(),
				Status: "failed", Error: valErr.Error(),
				ClaudeProjectPath: claudeProjectPath(session),
			})
			logClaudeSessionPre("validator", 1, "")
			logAgentEvent("agent_failed", "validator", 1, harness.TokenUsage{}, valErr)
			emit(Event{Type: EventAgentFailed, AgentID: "validator", Err: valErr})
		} else {
			validationOutput = valResult.Output
			if valResult.SessionID != "" {
				lastSessionID = valResult.SessionID
			}
			discValLogCopy, cpErr := agent.CopySessionLog(session, cwd, valResult.SessionID, "validator_session.jsonl")
			if cpErr != nil {
				logger.Warn("copy session log", "agent", "validator", "err", cpErr)
			}
			writeArtifactJSON(session, "validator_meta.json", agent.StepMeta{
				AgentID: "validator", ModelRef: e.Config.Worker.Model, StartTime: valStart, EndTime: time.Now(),
				ClaudeSessionID: valResult.SessionID, Status: "done",
				InputTokens: valResult.Usage.Input, OutputTokens: valResult.Usage.Output,
				ClaudeProjectPath:    claudeProjectPath(session),
				ClaudeSessionLogPath: discValLogCopy,
			})
			logClaudeSession("validator", 1, valResult.SessionID, discValLogCopy)
			logAgentEvent("agent_done", "validator", 1, valResult.Usage, nil)
			emit(Event{Type: EventAgentDone, AgentID: "validator",
				InputTokens: valResult.Usage.Input, OutputTokens: valResult.Usage.Output})
		}
	}

	// Parse validation output into structured result
	valParsed := agent.ParseValidationOutput(validationOutput)
	writeArtifact(session, "worker_validation.txt", validationOutput)

	// --- Commit message generation ---
	// back to a generic message. Only attempted when there is a worktree to
	// commit and a session to continue.
	semanticMsg := ""
	if wt.Path != "" && lastSessionID != "" {
		msgResult, msgErr := workerRunner.RunContinue(ctx, lastSessionID, agent.CommitMessagePrompt(), nil)
		if msgErr != nil {
			slog.Warn("commit message generation failed — using fallback", "err", msgErr)
		} else {
			parsed, parseErr := agent.ParseCommitMessage(msgResult.Output)
			if parseErr != nil {
				slog.Warn("commit message parse failed — using fallback", "err", parseErr)
			} else {
				semanticMsg = parsed
			}
		}
	}

	// buildCommitMsg returns the full commit message: the semantic summary (or a
	// generic fallback) followed by a compact run-ID trailer on its own paragraph.
	buildCommitMsg := func(fallbackPrefix string) string {
		msg := semanticMsg
		if msg == "" {
			msg = fallbackPrefix + ": Orqestra automated run"
		}
		return msg + "\n\nrun: " + runID + " by Orqestra"
	}

	// Derive run status from parsed validation verdict
	status := StatusSuccess
	if valParsed.Verdict == agent.VerdictFail {
		status = StatusFailed
	}

	// --- Post-run worktree commit + merge ---
	if wt.Path != "" {
		commitMsg := buildCommitMsg("feat")
		committed, commitErr := wt.CommitAll(ctx, commitMsg)
		if commitErr != nil {
			slog.Warn("worktree commit failed — skipping merge", "err", commitErr)
		} else if !committed {
			slog.Info("worktree: nothing to commit — merge skipped")
		}

		if committed && commitErr == nil {
			mergeResult, mergeErr := wt.MergeInto(ctx, targetBranch, buildCommitMsg("merge"))
			if mergeErr != nil {
				slog.Warn("worktree merge failed", "err", mergeErr)
				// Non-fatal: leave worktree branch intact for manual resolution
			} else if !mergeResult.Merged {
				// Conflicts — gate the user
				emit(Event{
					Type: EventMergeConflict,
					MergeConflict: MergeConflictInfo{
						WorktreeBranch: wt.Branch,
						TargetBranch:   targetBranch,
						ConflictFiles:  mergeResult.ConflictFiles,
					},
				})
				logger.Warn("merge_conflict", "worktree_branch", wt.Branch, "target_branch", targetBranch, "files", len(mergeResult.ConflictFiles))
				select {
				case decision := <-decisions:
					if decision.Type == DecisionMergeAbort {
						slog.Info("merge aborted by user", "branch", wt.Branch)
					}
					// Any other decision: user resolved externally or accepted abort
				case <-ctx.Done():
					// Context cancelled — leave worktree branch as-is
				}
			}
		}

		// Remove the worktree directory regardless of merge outcome
		if rmErr := wt.Remove(context.Background(), true); rmErr != nil {
			slog.Warn("worktree cleanup failed", "err", rmErr)
		}
	}

	// --- Completion ---
	emit(Event{Type: EventPhaseChange, Phase: PhaseDone})
	logger.Info("phase", "phase", string(PhaseDone))
	emit(Event{Type: EventComplete, Phase: PhaseDone,
		FinalPlan:        finalPlanMarkdown,
		WorkerValidation: validationOutput,
		Status:           status,
		RunDir:           session.Path,
	})
	logger.Info("run_complete", "status", string(status))
}

// truncateMsg truncates s to maxLen characters, collapsing newlines to spaces.
func truncateMsg(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

// writeArtifact writes a string artifact to the session directory.
func writeArtifact(session agent.SessionDir, name string, content string) {
	if session.Path == "" {
		return
	}
	if err := session.WriteArtifact(name, []byte(content)); err != nil {
		slog.Error("write artifact", "path", session.ArtifactPath(name), "err", err)
	}
}

// writeArtifactJSON marshals a value to JSON and writes it to the session directory.
func writeArtifactJSON(session agent.SessionDir, name string, v any) {
	if session.Path == "" {
		return
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		slog.Error("marshal artifact", "name", name, "err", err)
		return
	}
	if err := session.WriteArtifact(name, data); err != nil {
		slog.Error("write artifact", "path", session.ArtifactPath(name), "err", err)
	}
}

// claudeProjectPath returns the Claude project directory for the session's repo.
// session.Path must be at <repoPath>/.orqestra/sessions/<name>/.
func claudeProjectPath(session agent.SessionDir) string {
	if session.Path == "" {
		return ""
	}
	repoPath := filepath.Dir(filepath.Dir(filepath.Dir(session.Path)))
	resolved, err := filepath.EvalSymlinks(repoPath)
	if err != nil {
		resolved = repoPath
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects", harness.CwdToDash(resolved))
}

// DefaultRunDirFactory returns a RunDirFactory that creates session directories
// under the current working directory.
func DefaultRunDirFactory() RunDirFactory {
	return func(slug string) (agent.SessionDir, error) {
		cwd, err := os.Getwd()
		if err != nil {
			return agent.SessionDir{}, fmt.Errorf("get working directory: %w", err)
		}
		return agent.NewSessionDir(cwd, slug)
	}
}
