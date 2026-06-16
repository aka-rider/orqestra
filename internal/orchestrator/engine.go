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
	"github.com/xiii/orqestra/internal/mcp"
)

// RestartInput carries context for restarting a failed or incomplete run.
type RestartInput struct {
	RunPath string            // session directory of the original run
	Phase   RestartPhase      // which phase to restart from
}

// Input is the user's request to the orchestrator.
type Input struct {
	Prompt      string
	RestartFrom RestartInput
	Setup       PipelineSetup // optional pipeline configuration (set by TUI setup panel)
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
	Researcher harness.Runner
	Architect  harness.Runner
	Critic     harness.Runner
	Worker     harness.Runner
}

// resolveAgentMeta builds AgentMeta from the config for the given model ref.
// Returns a partially-populated AgentMeta (with ModelRef set) if the model is
// not found — never a silent fallback to a different model.
func resolveAgentMeta(cfg *config.Config, modelRef string) AgentMeta {
	meta := AgentMeta{ModelRef: modelRef}
	if cfg == nil || modelRef == "" {
		return meta
	}
	mc, ok := cfg.ModelMeta(modelRef)
	if !ok {
		return meta
	}
	meta.ModelDisplay = mc.Model
	meta.Provider = mc.Provider
	meta.ContextWindow = mc.ContextWindow
	return meta
}

// Engine is the hardcoded Go orchestrator that runs the full pipeline.
type Engine struct {
	Config         *config.Config
	RepoPath       string // canonical project root (from .orqestra or .git detection)
	Runners        Runners
	RunDirFactory  RunDirFactory
	QuestionBridge *mcp.QuestionBridge
	// WorktreeRunnerFactory, when set, is called just before the worker phase to
	// create a Runner scoped to the worktree at the given path.
	// If nil, the default Runners.Worker is used with repo write access.
	WorktreeRunnerFactory func(worktreePath string) harness.Runner
}

// RunChannels provides bidirectional communication between Engine and TUI.
type RunChannels struct {
	Events        <-chan Event
	Decisions     chan<- Decision
	StreamUpdates <-chan StreamEntry
	History       *StreamHistoryStore
}

// Start launches the pipeline in a goroutine. Returns channels immediately.
func (e *Engine) Start(ctx context.Context, input Input) RunChannels {
	events := make(chan Event, 16)
	decisions := make(chan Decision, 1)
	rawStream := make(chan harness.Event, 512)
	streamEntries := make(chan StreamEntry, 512)
	history := NewStreamHistoryStore()
	capture := newStreamCapture(history)

	go func() {
		defer close(streamEntries)
		for u := range rawStream {
			var entry StreamEntry
			switch {
			case u.IsDelta:
				entry = StreamEntry{Kind: EntryDelta, Text: u.Text}
			case u.Text != "":
				entry = StreamEntry{Kind: EntryText, Text: u.Text}
			case u.Tool != "":
				entry = StreamEntry{Kind: EntryToolUse, Tool: u.Tool, Detail: u.Detail}
			case u.Kind == harness.EventUsage:
				entry = StreamEntry{Kind: EntryStats, Stats: StreamStats{
					Input: u.Input, Output: u.Output, Valid: true,
				}}
			default:
				continue
			}
			select {
			case streamEntries <- entry:
			default:
			}
		}
	}()

	go func() {
		defer close(events)
		defer close(rawStream)
		e.run(ctx, input, events, decisions, capture, rawStream)
	}()

	return RunChannels{Events: events, Decisions: decisions, StreamUpdates: streamEntries, History: history}
}

// Run executes the full pipeline synchronously (legacy callback API).
func (e *Engine) Run(ctx context.Context, input Input, emit func(Event)) (Result, error) {
	channels := e.Start(ctx, input)

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

// SendAnswer delivers the user's answer to the waiting MCP bridge subprocess.
// No-op when QuestionBridge is nil.
func (e *Engine) SendAnswer(ans mcp.Answer) {
	if e.QuestionBridge == nil {
		return
	}
	e.QuestionBridge.SendAnswer(ans)
}

func runWithStreamConsumer[T any](
	call func(events chan<- harness.Event) (T, error),
	capture *streamCapture,
	out chan<- harness.Event,
) (T, error) {
	if capture == nil {
		return call(nil)
	}

	events := make(chan harness.Event, 256)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range events {
			if out != nil {
				select {
				case out <- ev:
				default:
				}
			}
			capture.OnUpdate(ev)
		}
	}()

	res, err := call(events)
	close(events)
	<-done
	return res, err
}

func runPlanner(ctx context.Context, planner *agent.Planner, prompt string, capture *streamCapture, out chan<- harness.Event) (agent.PlanResult, error) {
	return runWithStreamConsumer(func(events chan<- harness.Event) (agent.PlanResult, error) {
		return planner.Run(ctx, prompt, events)
	}, capture, out)
}

func continuePlanner(ctx context.Context, planner *agent.Planner, sessionID, prompt string, capture *streamCapture, out chan<- harness.Event) (agent.PlanResult, error) {
	return runWithStreamConsumer(func(events chan<- harness.Event) (agent.PlanResult, error) {
		return planner.Continue(ctx, sessionID, prompt, events)
	}, capture, out)
}

func runRunnerStreaming(ctx context.Context, runner harness.Runner, prompt, systemPrompt string, capture *streamCapture, out chan<- harness.Event) (harness.RunResult, error) {
	return runWithStreamConsumer(func(events chan<- harness.Event) (harness.RunResult, error) {
		runner.SetEvents(events)
		if systemPrompt != "" {
			// Send system prompt as initial message.
			runner.Post(systemPrompt)
		}
		runner.Post(prompt)
		// Wait for session to complete by reading from Receive().
		// The result is extracted from the EventSessionDone or EventError events.
		var result harness.RunResult
		for ev := range runner.Receive() {
			if ev.Kind == harness.EventError {
				result.Output = ev.Text
				result.Usage = harness.TokenUsage{Input: ev.Input, Output: ev.Output}
				result.SessionID = ev.SessionID
				return result, fmt.Errorf("runner error: %s", ev.Text)
			}
			if ev.Kind == harness.EventUsage {
				result.Usage = harness.TokenUsage{Input: ev.Input, Output: ev.Output}
			}
			if ev.Kind == harness.EventChunk && ev.Text != "" {
				result.Output += ev.Text
			}
		}
		result.SessionID = runner.SessionID()
		return result, nil
	}, capture, out)
}

func runRunnerContinue(ctx context.Context, runner harness.Runner, sessionID, prompt string, capture *streamCapture, out chan<- harness.Event) (harness.RunResult, error) {
	return runWithStreamConsumer(func(events chan<- harness.Event) (harness.RunResult, error) {
		runner.SetEvents(events)
		runner.Post(prompt)
		var result harness.RunResult
		for ev := range runner.Receive() {
			if ev.Kind == harness.EventError {
				result.Output = ev.Text
				result.Usage = harness.TokenUsage{Input: ev.Input, Output: ev.Output}
				result.SessionID = ev.SessionID
				return result, fmt.Errorf("runner error: %s", ev.Text)
			}
			if ev.Kind == harness.EventUsage {
				result.Usage = harness.TokenUsage{Input: ev.Input, Output: ev.Output}
			}
			if ev.Kind == harness.EventChunk && ev.Text != "" {
				result.Output += ev.Text
			}
		}
		result.SessionID = runner.SessionID()
		return result, nil
	}, capture, out)
}

func (e *Engine) run(ctx context.Context, input Input, events chan<- Event, decisions <-chan Decision, stream *streamCapture, streamOut chan<- harness.Event) {
	emit := func(ev Event) {
		select {
		case events <- ev:
		case <-ctx.Done():
		}
	}

	// Resolve and validate pipeline setup.
	setup := resolveSetup(input)
	if err := setup.Validate(); err != nil {
		emit(Event{Type: EventError, Err: fmt.Errorf("pipeline setup: %w", err)})
		return
	}

	// Create run directory
	var session agent.SessionDir
	var isRestart bool
	var restartSrc string
	if input.RestartFrom.RunPath != "" {
		isRestart = true
		restartSrc = input.RestartFrom.RunPath
	}
	if e.RunDirFactory != nil {
		var err error
		session, err = e.RunDirFactory("run")
		if err != nil {
			emit(Event{Type: EventError, Err: fmt.Errorf("create run directory: %w", err)})
			return
		}
	}

	if session.Path != "" {
		// Restart: copy completed artifacts from the original run.
		if isRestart && restartSrc != "" {
			if err := copyCompletedArtifacts(restartSrc, session.Path); err != nil {
				slog.Warn("restart artifact copy failed", "err", err)
			}
		}
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
	logger.Info("run started", "prompt_len", len(input.Prompt))

	// --- Per-agent lifecycle log helpers ---
	agentStart := map[string]time.Time{}

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
	var draftMarkdownForPlanning string // shared between Research and Planning phases
	var planSessionID string            // function scope — survives across gate loop iterations

	// Pre-resolve model metadata for session artifacts (StepMeta persistence).
	archMeta := resolveAgentMeta(e.Config, e.Config.Architect.Model)
	critMeta := resolveAgentMeta(e.Config, e.Config.Critic.Model)
	workMeta := resolveAgentMeta(e.Config, e.Config.Worker.Model)

	// --- Restart: skip completed phases ---
	if isRestart && restartSrc != "" {
		// Load final_plan.md from the copied artifacts if it exists.
		if data := restartReadStringArtifact(restartSrc, "final_plan.md"); data != "" {
			finalPlanMarkdown = data
		}
		// Load planSessionID from the architect meta if it exists.
		if planMeta := restartReadStringArtifact(restartSrc, "architect_meta.json"); planMeta != "" {
			var meta agent.StepMeta
			if json.Unmarshal([]byte(planMeta), &meta) == nil && meta.ClaudeSessionID != "" {
				planSessionID = meta.ClaudeSessionID
			}
		}
		// Load critic report if critic ran (kept for potential future use).
		_ = restartReadStringArtifact(restartSrc, "critic_report.md")

		// Determine which phase to skip to based on the restart phase.
		// (goto-based restart has been replaced by if-conditionals around phase calls.)
		switch input.RestartFrom.Phase {
		case RestartDeliberation:
			// Research completed — deliberation will run below.
		case RestartExecution, RestartValidation:
			// Research + deliberation completed — skip to plan gate.
			// The if-conditionals below will skip both research and deliberation.
		default:
			// Unknown or empty phase — run from the beginning.
		}
	}

	// --- Research ---
	if !isRestart || input.RestartFrom.Phase == RestartResearch {
		researchPrompt := guardPrompt(researcherInput, input.Prompt, "researcher")
		res, ok := e.runResearch(ctx, session, emit, logger, agentStart, stream, streamOut, researchPrompt)
		if !ok {
			return
		}
		draftMarkdownForPlanning = res.DraftMarkdown
	} else {
		// Research skipped — emit skipped event.
		emit(Event{Type: EventAgentSkipped, AgentID: "researcher"})
	}

	// --- GateAfterResearch (optional) ---
	if setup.HumanGates.Active(GateAfterResearch) {
		// Research gate uses the draft as the plan for review.
		planForGate := draftMarkdownForPlanning
		if planForGate == "" {
			planForGate = input.Prompt
		}
		_, _, _, err := e.runGateLoop(ctx, emit, decisions, GateAfterResearch,
			session, planForGate, "", nil, "", stream, streamOut)
		if err != nil {
			if err == errGateCancelled {
				emit(Event{Type: EventComplete, Phase: PhaseDone})
				logger.Info("run_complete", "status", "cancelled")
				return
			}
			// Non-cancellation error: log and continue.
			logger.Warn("research gate error", "err", err)
		}
	}

	// --- Planning + Critic + Revision ---
	if !isRestart || input.RestartFrom.Phase == RestartResearch || input.RestartFrom.Phase == "" {
		// Restore planSessionID from architect metadata when restarting from critic/worker.
		if isRestart && planSessionID == "" {
			if planMeta := restartReadStringArtifact(restartSrc, "architect_meta.json"); planMeta != "" {
				var meta agent.StepMeta
				if json.Unmarshal([]byte(planMeta), &meta) == nil && meta.ClaudeSessionID != "" {
					planSessionID = meta.ClaudeSessionID
				}
			}
		}
		delResult, ok := e.runDeliberation(ctx, session, emit, logger, agentStart, stream, streamOut,
			draftMarkdownForPlanning, cwd, input.Prompt, archMeta, critMeta)
		if !ok {
			return
		}
		finalPlanMarkdown = delResult.PlanMarkdown
		finalPlanWarnings = delResult.PlanWarnings
		planSessionID = delResult.PlanSessionID
		_ = delResult.CriticReport // kept for potential future use
	} else if isRestart {
		// Restart from critic/worker — plan was already restored above.
		if finalPlanMarkdown == "" {
			finalPlanMarkdown = restartReadStringArtifact(restartSrc, "final_plan.md")
		}
	} else {
		// Deliberation disabled — emit skipped event.
		emit(Event{Type: EventAgentSkipped, AgentID: "architect"})
	}

	// --- Plan Approval Gate ---
	writeArtifact(session, "final_plan.md", finalPlanMarkdown)
	if setup.HumanGates.Active(GateAfterDeliberation) {
		dec, fp, psid, err := e.runGateLoop(ctx, emit, decisions, GateAfterDeliberation,
			session, finalPlanMarkdown, session.ArtifactPath("final_plan.md"), finalPlanWarnings, planSessionID, stream, streamOut)
		if err != nil {
			if err == errGateCancelled {
				emit(Event{Type: EventComplete, Phase: PhaseDone})
				logger.Info("run_complete", "status", "cancelled")
				return
			}
			emit(Event{Type: EventError, Err: fmt.Errorf("gate: %w", err)})
			return
		}
		finalPlanMarkdown = fp
		planSessionID = psid
		finalPlanWarnings = agent.CheckPlanHealth(finalPlanMarkdown)
		_ = dec // used by TUI via events
	}

	// Save approved plan
	writeArtifact(session, "final_plan.md", finalPlanMarkdown)

	// --- GateAfterExecution (optional pause gate) ---
	if setup.HumanGates.Active(GateAfterExecution) && setup.Execution {
		_, _, _, err := e.runGateLoop(ctx, emit, decisions, GateAfterExecution,
			session, "", "", nil, "", stream, streamOut)
		if err != nil {
			if err == errGateCancelled {
				emit(Event{Type: EventComplete, Phase: PhaseDone})
				logger.Info("run_complete", "status", "cancelled")
				return
			}
			logger.Warn("execution gate error", "err", err)
		}
	}

	// --- Worker Execution ---
	var execResult *executionResult
	if setup.Execution {
		var ok bool
		execResult, ok = e.runExecution(ctx, session, emit, logger, agentStart, stream, streamOut,
			finalPlanMarkdown, workMeta)
		if !ok {
			return
		}
		finalPlanMarkdown = execResult.WorkOutput // not used after execution, but kept for completeness
		_ = execResult.ValParsed                  // validation result available for future use
		_ = execResult.Status                     // status available for future use
	} else {
		// Execution disabled — emit skipped event.
		emit(Event{Type: EventAgentSkipped, AgentID: "worker"})
	}

	// --- Completion ---
	emit(Event{Type: EventPhaseChange, Phase: PhaseDone})
	logger.Info("phase", "phase", string(PhaseDone))
	// Use execution result values if available, otherwise defaults.
	var workerValidation string
	var runStatus RunStatus = StatusSuccess
	if setup.Execution {
		// execResult is in scope from the execution block above.
		workerValidation = execResult.ValidationOutput
		runStatus = execResult.Status
	}
	emit(Event{Type: EventComplete, Phase: PhaseDone,
		FinalPlan:        finalPlanMarkdown,
		WorkerValidation: workerValidation,
		Status:           runStatus,
		RunDir:           session.Path,
	})
	logger.Info("run_complete", "status", string(runStatus))
}

// logAgentEvent logs a per-agent lifecycle event (started/done/failed).
func logAgentEvent(logger *slog.Logger, event, agentID string, attempt int, usage harness.TokenUsage, err error, agentStart map[string]time.Time) {
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

// logClaudeSession logs the Claude session ID and project path for an agent run.
func logClaudeSession(logger *slog.Logger, agentID string, attempt int, sessionID, sessionLogCopy string, session agent.SessionDir) {
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

// copySessionLog copies a Claude session JSONL log to the session directory.
func copySessionLog(s agent.SessionDir, repoPath, sessionID, destName string) (string, error) {
	return agent.CopySessionLog(s, repoPath, sessionID, destName, harness.ResolveSessionLogPath)
}

// logClaudeSessionPre logs the Claude session ID without a session log copy.
func logClaudeSessionPre(logger *slog.Logger, agentID string, attempt int, sessionID string, session agent.SessionDir) {
	if sessionID == "" {
		logger.Warn("claude_session_missing", "agent", agentID, "attempt", attempt)
		return
	}
	logger.Info("claude_session",
		"agent", agentID, "attempt", attempt,
		"session_id", sessionID,
		"project_path", claudeProjectPath(session))
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
// under the given project root.
func DefaultRunDirFactory(repoPath string) RunDirFactory {
	return func(slug string) (agent.SessionDir, error) {
		return agent.NewSessionDir(repoPath, slug)
	}
}

// copyDir copies a single file from src to dst.
func copyDir(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("creating directory for %s: %w", dst, err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	return nil
}

// restartReadStringArtifact reads a file as a string, returning empty on any error.
func restartReadStringArtifact(dir, name string) string {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return string(data)
}

// restartFileExists reports whether the given path exists and is not a directory.
func restartFileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}
