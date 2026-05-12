package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/plan"
)

// stepMeta is the per-agent metadata persisted as JSON in the session directory.
type stepMeta struct {
	AgentID         string    `json:"agent_id"`
	ModelRef        string    `json:"model_ref,omitempty"`
	StartTime       time.Time `json:"start_time"`
	EndTime         time.Time `json:"end_time"`
	ClaudeSessionID string    `json:"claude_session_id,omitempty"`
	Status          string    `json:"status"` // "done" or "failed"
	Error           string    `json:"error,omitempty"`
	InputTokens     int64     `json:"input_tokens"`
	OutputTokens    int64     `json:"output_tokens"`
}

// Activity records a single tool invocation observed in the agent stream.
type Activity struct {
	Tool   string // e.g. "Read", "Bash", "Write"
	Detail string // human-readable context, e.g. file path or truncated command
}

const maxActivities = 20

// StreamBuffer is a concurrent-safe line buffer shared between the orchestrator
// (writer) and the TUI (reader). The TUI polls it on tick to avoid channel
// backpressure that would block the subprocess.
type StreamBuffer struct {
	mu         sync.Mutex
	lines      []string
	agentID    string
	maxLines   int
	activities []Activity
}

// NewStreamBuffer creates a StreamBuffer with the given line capacity.
func NewStreamBuffer(maxLines int) *StreamBuffer {
	if maxLines <= 0 {
		maxLines = 200
	}
	return &StreamBuffer{maxLines: maxLines}
}

// SetAgent resets the buffer for a new active agent.
func (sb *StreamBuffer) SetAgent(id string) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.agentID = id
	sb.lines = nil
	sb.activities = nil
}

// Append adds text to the buffer, accumulating into the current line
// until a newline is encountered.
func (sb *StreamBuffer) Append(text string) {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	for len(text) > 0 {
		nlIdx := strings.IndexByte(text, '\n')
		if nlIdx == -1 {
			if len(sb.lines) == 0 {
				sb.lines = append(sb.lines, text)
			} else {
				sb.lines[len(sb.lines)-1] += text
			}
			break
		}
		fragment := text[:nlIdx]
		if len(sb.lines) == 0 {
			sb.lines = append(sb.lines, fragment)
		} else {
			sb.lines[len(sb.lines)-1] += fragment
		}
		sb.lines = append(sb.lines, "")
		text = text[nlIdx+1:]
	}

	if len(sb.lines) > sb.maxLines {
		sb.lines = sb.lines[len(sb.lines)-sb.maxLines:]
	}
}

// AppendActivity records a tool invocation in the activity ring.
func (sb *StreamBuffer) AppendActivity(tool, detail string) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.activities = append(sb.activities, Activity{Tool: tool, Detail: detail})
	if len(sb.activities) > maxActivities {
		sb.activities = sb.activities[len(sb.activities)-maxActivities:]
	}
}

// Snapshot returns the current agent ID, a copy of buffered lines, and recent activities.
func (sb *StreamBuffer) Snapshot() (agentID string, lines []string, activities []Activity) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	cp := make([]string, len(sb.lines))
	copy(cp, sb.lines)
	act := make([]Activity, len(sb.activities))
	copy(act, sb.activities)
	return sb.agentID, cp, act
}

// EventType classifies orchestrator events emitted to the TUI.
type EventType int

const (
	EventPhaseChange EventType = iota
	EventAgentStarted
	EventAgentDone
	EventAgentFailed
	EventAgentCancelled
	EventAgentOutput
	EventPlanReady
	EventGateRequest
	EventComplete
	EventError
	EventRunDirReady  // emitted once after session dir is created
	EventChatResponse // emitted when architect answers without revising the plan
	EventUserQuestion // emitted when an agent asks the user a question via MCP
)

// Phase represents the current pipeline phase.
type Phase string

const (
	PhaseGateway        Phase = "gateway"
	PhaseResearching    Phase = "researching"
	PhasePlanning       Phase = "planning"
	PhaseExecuting      Phase = "executing"
	PhaseSelfValidating Phase = "self-validating"
	PhaseDone           Phase = "done"
)

// GateType identifies which interactive gate the pipeline is waiting at.
type GateType int

const (
	GateGatewayCoach GateType = iota
	GatePlanApproval
)

// GateRequest is emitted when the pipeline needs user input.
type GateRequest struct {
	Type              GateType
	GatewayResult     agent.GatewayResult
	FinalPlanMarkdown string // for GatePlanApproval
	PlanFilePath      string // absolute path to plan.md on disk (for external editor)
	PlanDiff          string // unified diff from git micro-repo (empty if no history)
}

// DecisionType classifies user decisions at gates.
type DecisionType int

const (
	DecisionApprove DecisionType = iota
	DecisionEdit
	DecisionSkip
	DecisionCancel
	DecisionComment // comment-only refinement at plan gate
)

// Decision is sent from TUI to pipeline at gates.
type Decision struct {
	Type          DecisionType
	EditedContent string
	Comment       string // for DecisionComment
}

// Event is emitted by the orchestrator to notify the TUI of progress.
type Event struct {
	Type        EventType
	Phase       Phase
	AgentID     string
	Gate        GateRequest
	WorkOutput  string
	OutputChunk string
	Err         error

	// New pipeline fields
	ResearchDraft    string
	FinalPlan        string
	WorkerValidation string
	Status           RunStatus // set on EventComplete
	RunDir           string    // set on EventComplete

	// Token usage from the agent's RunResult. Set on EventAgentDone.
	InputTokens  int64
	OutputTokens int64

	// ChatText is set on EventChatResponse — architect answered without revising the plan.
	ChatText string

	// UserQuestion is set on EventUserQuestion.
	UserQuestion harness.MCPToolCall
}

// Input is the user's request to the orchestrator.
type Input struct {
	Prompt      string
	AutoApprove bool
	SkipGateway bool
	PlanFile    string // pre-loaded plan markdown (--plan flag)
	NoExecute   bool   // stop after plan gate
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

// Runners holds all CLIRunners for each agent role.
type Runners struct {
	Gateway    harness.CLIRunner
	Researcher harness.CLIRunner
	Architect  harness.CLIRunner
	Worker     harness.ContinuableRunner
}

// Engine is the hardcoded Go orchestrator that runs the full pipeline.
type Engine struct {
	Config         *config.Config
	Runners        Runners
	RunDirFactory  RunDirFactory
	QuestionBridge *harness.QuestionBridge
}

// RunChannels provides bidirectional communication between Engine and TUI.
type RunChannels struct {
	Events    <-chan Event
	Decisions chan<- Decision
	Stream    *StreamBuffer
}

// Start launches the pipeline in a goroutine. Returns channels immediately.
func (e *Engine) Start(ctx context.Context, input Input) RunChannels {
	events := make(chan Event, 16)
	decisions := make(chan Decision, 1)
	stream := NewStreamBuffer(200)

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

func (e *Engine) run(ctx context.Context, input Input, events chan<- Event, decisions <-chan Decision, stream *StreamBuffer) {
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

	// --- Gateway Evaluation ---
	var architectInput string

	if input.SkipGateway {
		architectInput = input.Prompt
	} else if e.Runners.Gateway != nil {
		emit(Event{Type: EventPhaseChange, Phase: PhaseGateway})
		emit(Event{Type: EventAgentStarted, AgentID: "gateway"})
		stream.SetAgent("gateway")

		gw := agent.NewGateway(e.Runners.Gateway, e.Config.Gateway)
		gwStart := time.Now()

		// Single gateway call — questions are resolved via MCP AskUserQuestion tool
		// during RunStreaming, not via coaching loop.
		gwResult, gwUsage, gwSessionID, err := gw.Evaluate(ctx, input.Prompt, &streamWriter{buf: stream})
		if err != nil {
			writeArtifactJSON(session, "gateway_meta.json", stepMeta{
				AgentID: "gateway", ModelRef: e.Config.Gateway.Model, StartTime: gwStart, EndTime: time.Now(),
				ClaudeSessionID: gwSessionID, Status: "failed", Error: err.Error(),
			})
			emit(Event{Type: EventAgentFailed, AgentID: "gateway", Err: err})
			emit(Event{Type: EventError, Err: err})
			return
		}

		// Treat "coach" verdict as "accept" — questions were already resolved via MCP.
		if gwResult.Verdict == agent.GatewayVerdictCoach {
			slog.Warn("gateway returned 'coach' verdict, treating as 'accept' — questions should use AskUserQuestion MCP tool")
		}

		writeArtifactJSON(session, "gateway_meta.json", stepMeta{
			AgentID: "gateway", ModelRef: e.Config.Gateway.Model, StartTime: gwStart, EndTime: time.Now(),
			ClaudeSessionID: gwSessionID, Status: "done",
			InputTokens: gwUsage.InputTokens, OutputTokens: gwUsage.OutputTokens,
		})
		emit(Event{Type: EventAgentDone, AgentID: "gateway",
			InputTokens: gwUsage.InputTokens, OutputTokens: gwUsage.OutputTokens})
		architectInput = buildArchitectInput(input.Prompt, gwResult.Brief)
	} else {
		architectInput = input.Prompt
	}

	var finalPlanMarkdown string
	var planSessionID string // function scope — survives across gate loop iterations
	architect := agent.NewArchitect(e.Runners.Architect, e.Config.Architect)

	// Initialize plan version history (git micro-repo)
	var planRepo *plan.GitRepo
	if session.Path != "" {
		var repoErr error
		planRepo, repoErr = plan.NewGitRepo(session.Path)
		if repoErr != nil {
			slog.Warn("plan history unavailable — diff disabled", "err", repoErr)
		}
	}

	// If a pre-loaded plan was provided, skip research and planning
	if input.PlanFile != "" {
		finalPlanMarkdown = input.PlanFile
		if planRepo != nil {
			if err := planRepo.Commit(finalPlanMarkdown, "plan loaded from file"); err != nil {
				slog.Warn("plan commit failed", "err", err)
			}
		}
		goto planGate
	}

	// --- Research ---
	{
		emit(Event{Type: EventPhaseChange, Phase: PhaseResearching})
		emit(Event{Type: EventAgentStarted, AgentID: "researcher"})
		stream.SetAgent("researcher")

		researcher := agent.NewResearcher(e.Runners.Researcher, e.Config.Researcher)
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
			draft, draftUsage, researchSessionID, err = researcher.ResearchStreaming(ctx, architectInput, &streamWriter{buf: stream})
			if err == nil {
				break
			}
			if attempt < researchAttempts {
				slog.Warn("researcher attempt failed, retrying", "attempt", attempt, "err", err)
				stream.SetAgent("researcher") // reset stream for retry
			}
		}
		if err != nil {
			writeArtifactJSON(session, "researcher_meta.json", stepMeta{
				AgentID: "researcher", ModelRef: e.Config.Researcher.Model, StartTime: researchStart, EndTime: time.Now(),
				ClaudeSessionID: researchSessionID, Status: "failed", Error: err.Error(),
			})
			emit(Event{Type: EventAgentFailed, AgentID: "researcher", Err: err})
			emit(Event{Type: EventError, Err: fmt.Errorf("research: %w", err)})
			return
		}

		writeArtifact(session, "researcher_draft.md", draft.Markdown)
		writeArtifactJSON(session, "researcher_meta.json", stepMeta{
			AgentID: "researcher", ModelRef: e.Config.Researcher.Model, StartTime: researchStart, EndTime: time.Now(),
			ClaudeSessionID: researchSessionID, Status: "done",
			InputTokens: draftUsage.InputTokens, OutputTokens: draftUsage.OutputTokens,
		})
		emit(Event{Type: EventAgentDone, AgentID: "researcher",
			InputTokens: draftUsage.InputTokens, OutputTokens: draftUsage.OutputTokens,
			ResearchDraft: draft.Markdown})

		// --- Planning ---
		emit(Event{Type: EventPhaseChange, Phase: PhasePlanning})
		emit(Event{Type: EventAgentStarted, AgentID: "architect"})
		stream.SetAgent("architect")

		planStart := time.Now()

		architectAttempts := e.Config.Retry.ArchitectAttempts
		if architectAttempts < 1 {
			architectAttempts = 1
		}

		var planResult agent.RawPlan
		var planUsage harness.TokenUsage
		var planErr error
		for attempt := 1; attempt <= architectAttempts; attempt++ {
			var sid string
			planResult, planUsage, sid, planErr = architect.RefineStreaming(ctx, draft.Markdown, &streamWriter{buf: stream})
			if planErr == nil {
				planSessionID = sid
				break
			}
			if attempt < architectAttempts {
				slog.Warn("architect attempt failed, retrying", "attempt", attempt, "err", planErr)
				stream.SetAgent("architect") // reset stream for retry
			}
		}
		if planErr != nil {
			writeArtifactJSON(session, "architect_meta.json", stepMeta{
				AgentID: "architect", ModelRef: e.Config.Architect.Model, StartTime: planStart, EndTime: time.Now(),
				ClaudeSessionID: planSessionID, Status: "failed", Error: planErr.Error(),
			})
			emit(Event{Type: EventAgentFailed, AgentID: "architect", Err: planErr})
			emit(Event{Type: EventError, Err: fmt.Errorf("planning: %w", planErr)})
			return
		}

		writeArtifactJSON(session, "architect_meta.json", stepMeta{
			AgentID: "architect", ModelRef: e.Config.Architect.Model, StartTime: planStart, EndTime: time.Now(),
			ClaudeSessionID: planSessionID, Status: "done",
			InputTokens: planUsage.InputTokens, OutputTokens: planUsage.OutputTokens,
		})
		emit(Event{Type: EventAgentDone, AgentID: "architect",
			InputTokens: planUsage.InputTokens, OutputTokens: planUsage.OutputTokens})

		finalPlanMarkdown = planResult.Markdown
		if planRepo != nil {
			if err := planRepo.Commit(finalPlanMarkdown, "initial plan from architect"); err != nil {
				slog.Warn("plan commit failed", "err", err)
			}
		}
	}

planGate:
	// --- Plan Approval Gate ---
	emit(Event{Type: EventPlanReady, FinalPlan: finalPlanMarkdown})

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
			emit(Event{Type: EventGateRequest, Gate: GateRequest{
				Type:              GatePlanApproval,
				FinalPlanMarkdown: finalPlanMarkdown,
				PlanFilePath:      session.ArtifactPath("final_plan.md"),
				PlanDiff:          planDiff,
			}})

			select {
			case decision := <-decisions:
				switch decision.Type {
				case DecisionCancel:
					emit(Event{Type: EventComplete, Phase: PhaseDone})
					return
				case DecisionEdit:
					// Basic sanity check on edited content
					edited := strings.TrimSpace(decision.EditedContent)
					if !strings.HasPrefix(edited, "# Plan") {
						emit(Event{Type: EventError, Err: fmt.Errorf("edited plan must start with '# Plan'")})
						return
					}
					finalPlanMarkdown = edited
					if planRepo != nil {
						if err := planRepo.Commit(edited, "manual edit"); err != nil {
							slog.Warn("plan commit failed", "err", err)
						}
					}
					continue // re-show gate with edited plan
				case DecisionComment:
					emit(Event{Type: EventAgentStarted, AgentID: "architect"})
					stream.SetAgent("architect")
					revStart := time.Now()

					var chatResponse string
					var revisedPlan *agent.RawPlan
					var revisedUsage harness.TokenUsage
					var err error

					if planSessionID != "" {
						chatResponse, revisedPlan, revisedUsage, err = architect.ContinueSession(
							ctx, planSessionID, finalPlanMarkdown, decision.Comment, &streamWriter{buf: stream})
					} else {
						// Fallback for cold start (--plan flag) — no session to resume
						revised, revUsage, revSID, refineErr := architect.RefineWithCommentsStreaming(
							ctx, finalPlanMarkdown, decision.Comment, &streamWriter{buf: stream})
						if refineErr == nil {
							planSessionID = revSID
							revisedPlan = &revised
						}
						revisedUsage = revUsage
						err = refineErr
						chatResponse = ""
					}

					if err != nil {
						writeArtifactJSON(session, "architect_revision_meta.json", stepMeta{
							AgentID: "architect", ModelRef: e.Config.Architect.Model,
							StartTime: revStart, EndTime: time.Now(),
							ClaudeSessionID: planSessionID, Status: "failed", Error: err.Error(),
						})
						emit(Event{Type: EventAgentFailed, AgentID: "architect", Err: err})
						emit(Event{Type: EventError, Err: fmt.Errorf("architect revision: %w", err)})
						return
					}

					writeArtifactJSON(session, "architect_revision_meta.json", stepMeta{
						AgentID: "architect", ModelRef: e.Config.Architect.Model,
						StartTime: revStart, EndTime: time.Now(),
						ClaudeSessionID: planSessionID, Status: "done",
						InputTokens: revisedUsage.InputTokens, OutputTokens: revisedUsage.OutputTokens,
					})
					emit(Event{Type: EventAgentDone, AgentID: "architect",
						InputTokens: revisedUsage.InputTokens, OutputTokens: revisedUsage.OutputTokens})

					if revisedPlan != nil {
						finalPlanMarkdown = revisedPlan.Markdown
						if planRepo != nil {
							msg := commitMsg("revision", decision.Comment)
							if commitErr := planRepo.Commit(finalPlanMarkdown, msg); commitErr != nil {
								slog.Warn("plan commit failed", "err", commitErr)
							}
						}
					} else if chatResponse != "" {
						// Chat-only response — no plan change
						emit(Event{Type: EventChatResponse, ChatText: chatResponse})
					}
					continue // re-show gate with (possibly revised) plan
				case DecisionApprove:
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
		emit(Event{Type: EventComplete, Phase: PhaseDone,
			FinalPlan: finalPlanMarkdown,
			Status:    StatusSuccess,
			RunDir:    session.Path,
		})
		return
	}

	// --- Worker Execution ---
	emit(Event{Type: EventPhaseChange, Phase: PhaseExecuting})
	emit(Event{Type: EventAgentStarted, AgentID: "worker"})
	stream.SetAgent("worker")
	workerStart := time.Now()

	execPrompt := agent.BuildExecutionPromptFromPlan(finalPlanMarkdown)
	workResult, execErr := e.Runners.Worker.RunStreaming(ctx, execPrompt, "", &streamWriter{buf: stream})
	if execErr != nil {
		writeArtifactJSON(session, "worker_meta.json", stepMeta{
			AgentID: "worker", ModelRef: e.Config.Worker.Model, StartTime: workerStart, EndTime: time.Now(),
			Status: "failed", Error: execErr.Error(),
		})
		emit(Event{Type: EventAgentFailed, AgentID: "worker", Err: execErr})
		emit(Event{Type: EventError, Err: execErr})
		return
	}

	writeArtifact(session, "worker_output.txt", workResult.Output)
	writeArtifactJSON(session, "worker_meta.json", stepMeta{
		AgentID: "worker", ModelRef: e.Config.Worker.Model, StartTime: workerStart, EndTime: time.Now(),
		ClaudeSessionID: workResult.SessionID, Status: "done",
		InputTokens: workResult.Usage.InputTokens, OutputTokens: workResult.Usage.OutputTokens,
	})
	emit(Event{Type: EventAgentDone, AgentID: "worker", WorkOutput: workResult.Output,
		InputTokens: workResult.Usage.InputTokens, OutputTokens: workResult.Usage.OutputTokens})

	// --- Worker Self-Validation via Session Continuation ---
	emit(Event{Type: EventPhaseChange, Phase: PhaseSelfValidating})
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
		valResult, valErr := e.Runners.Worker.RunContinue(ctx, workResult.SessionID, validationPrompt, &streamWriter{buf: stream})
		if valErr != nil {
			slog.Warn("worker self-validation failed", "err", valErr)
			writeArtifactJSON(session, "validator_meta.json", stepMeta{
				AgentID: "validator", ModelRef: e.Config.Worker.Model, StartTime: valStart, EndTime: time.Now(),
				ClaudeSessionID: workResult.SessionID, Status: "failed", Error: valErr.Error(),
			})
			emit(Event{Type: EventAgentFailed, AgentID: "validator", Err: valErr})
			// Non-fatal: proceed with whatever output we have
		} else {
			validationOutput = valResult.Output
			writeArtifactJSON(session, "validator_meta.json", stepMeta{
				AgentID: "validator", ModelRef: e.Config.Worker.Model, StartTime: valStart, EndTime: time.Now(),
				ClaudeSessionID: valResult.SessionID, Status: "done",
				InputTokens: valResult.Usage.InputTokens, OutputTokens: valResult.Usage.OutputTokens,
			})
			emit(Event{Type: EventAgentDone, AgentID: "validator",
				InputTokens: valResult.Usage.InputTokens, OutputTokens: valResult.Usage.OutputTokens})
		}
	} else {
		slog.Warn("no session ID from worker — attempting disconnected validation")
		// Fallback: run validation as a new session (less effective but still useful)
		valResult, valErr := e.Runners.Worker.RunStreaming(ctx, validationPrompt, "", &streamWriter{buf: stream})
		if valErr != nil {
			slog.Warn("disconnected validation failed", "err", valErr)
			writeArtifactJSON(session, "validator_meta.json", stepMeta{
				AgentID: "validator", ModelRef: e.Config.Worker.Model, StartTime: valStart, EndTime: time.Now(),
				Status: "failed", Error: valErr.Error(),
			})
			emit(Event{Type: EventAgentFailed, AgentID: "validator", Err: valErr})
		} else {
			validationOutput = valResult.Output
			writeArtifactJSON(session, "validator_meta.json", stepMeta{
				AgentID: "validator", ModelRef: e.Config.Worker.Model, StartTime: valStart, EndTime: time.Now(),
				ClaudeSessionID: valResult.SessionID, Status: "done",
				InputTokens: valResult.Usage.InputTokens, OutputTokens: valResult.Usage.OutputTokens,
			})
			emit(Event{Type: EventAgentDone, AgentID: "validator",
				InputTokens: valResult.Usage.InputTokens, OutputTokens: valResult.Usage.OutputTokens})
		}
	}

	writeArtifact(session, "worker_validation.txt", validationOutput)

	// Check for failures in validation output
	status := StatusSuccess
	if strings.Contains(validationOutput, "❌") {
		status = StatusFailed
	}

	// --- Completion ---
	emit(Event{Type: EventPhaseChange, Phase: PhaseDone})
	emit(Event{Type: EventComplete, Phase: PhaseDone,
		FinalPlan:        finalPlanMarkdown,
		WorkerValidation: validationOutput,
		Status:           status,
		RunDir:           session.Path,
	})
}

// buildArchitectInput constructs the architect prompt from the raw user prompt and
// gateway brief.
func buildArchitectInput(rawPrompt string, brief agent.PromptBrief) string {
	var b strings.Builder
	b.WriteString(rawPrompt)
	if brief.EndState != "" {
		b.WriteString("\n\nExpected outcome: ")
		b.WriteString(brief.EndState)
	}
	if len(brief.Scope) > 0 {
		b.WriteString("\nScope: ")
		b.WriteString(strings.Join(brief.Scope, ", "))
	}
	if len(brief.NonScope) > 0 {
		b.WriteString("\nOut of scope: ")
		b.WriteString(strings.Join(brief.NonScope, ", "))
	}
	return b.String()
}

// commitMsg builds a git commit message from a prefix and a comment.
func commitMsg(prefix, comment string) string {
	if comment == "" {
		return prefix
	}
	if len(comment) > 50 {
		return prefix + ": " + comment[:50]
	}
	return prefix + ": " + comment
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

// streamWriter implements io.Writer and harness.ActivitySink by appending
// to a StreamBuffer.
type streamWriter struct {
	buf *StreamBuffer
}

func (w *streamWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		w.buf.Append(string(p))
	}
	return len(p), nil
}

// OnToolUse implements harness.ActivitySink.
func (w *streamWriter) OnToolUse(name, detail string) {
	w.buf.AppendActivity(name, detail)
}
