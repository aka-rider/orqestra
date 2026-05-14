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
	"github.com/xiii/orqestra/internal/worktree"
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
	mu             sync.Mutex
	lines          []string
	agentID        string
	maxLines       int
	activities     []Activity
	agentSnapshots map[string][]Activity
}

// NewStreamBuffer creates a StreamBuffer with the given line capacity.
func NewStreamBuffer(maxLines int) *StreamBuffer {
	if maxLines <= 0 {
		maxLines = 200
	}
	return &StreamBuffer{
		maxLines:       maxLines,
		agentSnapshots: make(map[string][]Activity),
	}
}

// SetAgent resets the buffer for a new active agent.
func (sb *StreamBuffer) SetAgent(id string) {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	if sb.agentID != "" && len(sb.activities) > 0 {
		snapshot := make([]Activity, len(sb.activities))
		copy(snapshot, sb.activities)
		sb.agentSnapshots[sb.agentID] = snapshot
	}

	sb.agentID = id
	sb.lines = nil
	sb.activities = nil
}

// AgentActivities returns a copy of the recorded activities for the given agent.
func (sb *StreamBuffer) AgentActivities(id string) []Activity {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	if sb.agentID == id {
		out := make([]Activity, len(sb.activities))
		copy(out, sb.activities)
		return out
	}

	activities, ok := sb.agentSnapshots[id]
	if !ok {
		return nil
	}
	out := make([]Activity, len(activities))
	copy(out, activities)
	return out
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
	EventRunDirReady   // emitted once after session dir is created
	EventChatResponse  // emitted when architect answers without revising the plan
	EventUserQuestion  // emitted when an agent asks the user a question via MCP
	EventMergeConflict // emitted when the post-run merge has conflicts
)

// Phase represents the current pipeline phase.
type Phase string

const (
	PhaseResearching    Phase = "researching"
	PhasePlanning       Phase = "planning"
	PhaseCritiquing     Phase = "critiquing"
	PhaseExecuting      Phase = "executing"
	PhaseSelfValidating Phase = "self-validating"
	PhaseDone           Phase = "done"
)

// GateType identifies which interactive gate the pipeline is waiting at.
type GateType int

const (
	GatePlanApproval GateType = iota
)

// GateRequest is emitted when the pipeline needs user input.
type GateRequest struct {
	Type              GateType
	FinalPlanMarkdown string // for GatePlanApproval
	PlanFilePath      string // absolute path to plan.md on disk (for external editor)
	PlanDiff          string // unified diff from git micro-repo (empty if no history)
	PlanWarnings      []string
	CriticReport      string // critic's review report, shown alongside the plan at gate
}

// DecisionType classifies user decisions at gates.
type DecisionType int

const (
	DecisionApprove DecisionType = iota
	DecisionEdit
	DecisionSkip
	DecisionCancel
	DecisionComment    // comment-only refinement at plan gate
	DecisionMergeAbort // abort the post-run merge, keep the worktree branch
)

// MergeConflictInfo is carried by EventMergeConflict.
type MergeConflictInfo struct {
	WorktreeBranch string   // branch that was merged (for display)
	TargetBranch   string   // branch that received the merge
	ConflictFiles  []string // list of conflicting files
}

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

	// MergeConflict is set on EventMergeConflict.
	MergeConflict MergeConflictInfo
}

// Input is the user's request to the orchestrator.
type Input struct {
	Prompt      string
	AutoApprove bool
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
	Researcher harness.CLIRunner
	Architect  harness.CLIRunner
	Critic     harness.CLIRunner
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

	// --- Research ---
	researcherInput := input.Prompt
	var finalPlanMarkdown string
	var finalPlanWarnings []string
	var criticReportMarkdown string
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
			if err := planRepo.Commit(finalPlanMarkdown, "user: plan loaded from file"); err != nil {
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
			draft, draftUsage, researchSessionID, err = researcher.ResearchStreaming(ctx, researcherInput, &streamWriter{buf: stream})
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
			planResult, planUsage, sid, planErr = architect.RefineStreaming(ctx, input.Prompt, draft.Markdown, &streamWriter{buf: stream})
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

		finalPlanWarnings = planResult.Warnings
		finalPlanMarkdown = planResult.Markdown
		if planRepo != nil {
			if err := planRepo.Commit(finalPlanMarkdown, "architect: initial plan"); err != nil {
				slog.Warn("plan commit failed", "err", err)
			}
		}
	}

	// --- Critic Review ---
	if e.Runners.Critic != nil {
		emit(Event{Type: EventPhaseChange, Phase: PhaseCritiquing})
		emit(Event{Type: EventAgentStarted, AgentID: "critic"})
		stream.SetAgent("critic")

		critic := agent.NewCritic(e.Runners.Critic, e.Config.Critic)
		criticStart := time.Now()

		criticAttempts := e.Config.Retry.CriticAttempts
		if criticAttempts < 1 {
			criticAttempts = 1
		}

		var criticResult agent.CriticReport
		var criticUsage harness.TokenUsage
		var criticSessionID string
		var criticErr error
		for attempt := 1; attempt <= criticAttempts; attempt++ {
			criticResult, criticUsage, criticSessionID, criticErr = critic.ReviewStreaming(ctx, input.Prompt, finalPlanMarkdown, &streamWriter{buf: stream})
			if criticErr == nil {
				break
			}
			if attempt < criticAttempts {
				slog.Warn("critic attempt failed, retrying", "attempt", attempt, "err", criticErr)
				stream.SetAgent("critic")
			}
		}
		if criticErr != nil {
			writeArtifactJSON(session, "critic_meta.json", stepMeta{
				AgentID: "critic", ModelRef: e.Config.Critic.Model, StartTime: criticStart, EndTime: time.Now(),
				ClaudeSessionID: criticSessionID, Status: "failed", Error: criticErr.Error(),
			})
			emit(Event{Type: EventAgentFailed, AgentID: "critic", Err: criticErr})
			emit(Event{Type: EventError, Err: fmt.Errorf("critic review: %w", criticErr)})
			return
		}

		writeArtifact(session, "critic_report.md", criticResult.Markdown)
		writeArtifactJSON(session, "critic_meta.json", stepMeta{
			AgentID: "critic", ModelRef: e.Config.Critic.Model, StartTime: criticStart, EndTime: time.Now(),
			ClaudeSessionID: criticSessionID, Status: "done",
			InputTokens: criticUsage.InputTokens, OutputTokens: criticUsage.OutputTokens,
		})
		emit(Event{Type: EventAgentDone, AgentID: "critic",
			InputTokens: criticUsage.InputTokens, OutputTokens: criticUsage.OutputTokens})

		criticReportMarkdown = criticResult.Markdown

		// --- Architect Second Pass (critic feedback) ---
		emit(Event{Type: EventAgentStarted, AgentID: "architect"})
		stream.SetAgent("architect")
		revStart := time.Now()

		chatResponse, revisedPlan, revisedUsage, revErr := architect.ContinueWithCriticReport(
			ctx, planSessionID, finalPlanMarkdown, criticResult.Markdown, &streamWriter{buf: stream})
		_ = chatResponse // architect may include reasoning alongside plan revision

		if revErr != nil {
			writeArtifactJSON(session, "architect_critic_revision_meta.json", stepMeta{
				AgentID: "architect", ModelRef: e.Config.Architect.Model,
				StartTime: revStart, EndTime: time.Now(),
				ClaudeSessionID: planSessionID, Status: "failed", Error: revErr.Error(),
			})
			emit(Event{Type: EventAgentFailed, AgentID: "architect", Err: revErr})
			emit(Event{Type: EventError, Err: fmt.Errorf("architect critic revision: %w", revErr)})
			return
		}

		writeArtifactJSON(session, "architect_critic_revision_meta.json", stepMeta{
			AgentID: "architect", ModelRef: e.Config.Architect.Model,
			StartTime: revStart, EndTime: time.Now(),
			ClaudeSessionID: planSessionID, Status: "done",
			InputTokens: revisedUsage.InputTokens, OutputTokens: revisedUsage.OutputTokens,
		})
		emit(Event{Type: EventAgentDone, AgentID: "architect",
			InputTokens: revisedUsage.InputTokens, OutputTokens: revisedUsage.OutputTokens})

		if revisedPlan != nil {
			finalPlanMarkdown = revisedPlan.Markdown
			finalPlanWarnings = revisedPlan.Warnings
			if planRepo != nil {
				if commitErr := planRepo.Commit(finalPlanMarkdown, "architect: Re: critic feedback"); commitErr != nil {
					slog.Warn("plan commit failed", "err", commitErr)
				}
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
				PlanWarnings:      finalPlanWarnings,
				CriticReport:      criticReportMarkdown,
			}})

			select {
			case decision := <-decisions:
				switch decision.Type {
				case DecisionCancel:
					emit(Event{Type: EventComplete, Phase: PhaseDone})
					return
				case DecisionEdit:
					edited := strings.TrimSpace(decision.EditedContent)
					finalPlanMarkdown = edited
					finalPlanWarnings = agent.CheckPlanHealth(edited)
					if planRepo != nil {
						if err := planRepo.Commit(edited, "user: manual edit"); err != nil {
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
						finalPlanWarnings = revisedPlan.Warnings
						if planRepo != nil {
							msg := commitMsg("architect: Re", decision.Comment)
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
	workResult, execErr := workerRunner.RunStreaming(ctx, execPrompt, "", &streamWriter{buf: stream})

	// Clean up worktree on failure or cancellation.
	if execErr != nil {
		if wt.Path != "" {
			if rmErr := wt.Remove(context.Background(), true); rmErr != nil {
				slog.Warn("worktree cleanup failed", "err", rmErr)
			}
		}
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

	// lastSessionID tracks the most recent session continuation — used for
	// commit message generation after validation completes.
	lastSessionID := workResult.SessionID

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
		valResult, valErr := workerRunner.RunContinue(ctx, workResult.SessionID, validationPrompt, &streamWriter{buf: stream})
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
			if valResult.SessionID != "" {
				lastSessionID = valResult.SessionID
			}
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
		valResult, valErr := workerRunner.RunStreaming(ctx, validationPrompt, "", &streamWriter{buf: stream})
		if valErr != nil {
			slog.Warn("disconnected validation failed", "err", valErr)
			writeArtifactJSON(session, "validator_meta.json", stepMeta{
				AgentID: "validator", ModelRef: e.Config.Worker.Model, StartTime: valStart, EndTime: time.Now(),
				Status: "failed", Error: valErr.Error(),
			})
			emit(Event{Type: EventAgentFailed, AgentID: "validator", Err: valErr})
		} else {
			validationOutput = valResult.Output
			if valResult.SessionID != "" {
				lastSessionID = valResult.SessionID
			}
			writeArtifactJSON(session, "validator_meta.json", stepMeta{
				AgentID: "validator", ModelRef: e.Config.Worker.Model, StartTime: valStart, EndTime: time.Now(),
				ClaudeSessionID: valResult.SessionID, Status: "done",
				InputTokens: valResult.Usage.InputTokens, OutputTokens: valResult.Usage.OutputTokens,
			})
			emit(Event{Type: EventAgentDone, AgentID: "validator",
				InputTokens: valResult.Usage.InputTokens, OutputTokens: valResult.Usage.OutputTokens})
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
	emit(Event{Type: EventComplete, Phase: PhaseDone,
		FinalPlan:        finalPlanMarkdown,
		WorkerValidation: validationOutput,
		Status:           status,
		RunDir:           session.Path,
	})
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
