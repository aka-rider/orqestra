package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

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

// GatewayAnswer holds a user's response to a coaching question.
type GatewayAnswer struct {
	QuestionIndex int
	Answer        string
}

// Decision is sent from TUI to pipeline at gates.
type Decision struct {
	Type           DecisionType
	GatewayAnswers []GatewayAnswer
	EditedContent  string
	Comment        string // for DecisionComment
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

	// Token usage from the agent's RunResult. Set on EventAgentDone.
	InputTokens  int64
	OutputTokens int64
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
	Planner    harness.CLIRunner
	Worker     harness.ContinuableRunner
}

// Engine is the hardcoded Go orchestrator that runs the full pipeline.
type Engine struct {
	Config        *config.Config
	Runners       Runners
	RunDirFactory RunDirFactory
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
			result = Result{
				Status:           StatusSuccess,
				FinalPlan:        event.FinalPlan,
				WorkerValidation: event.WorkerValidation,
			}
		}
	}
	if lastErr != nil {
		return Result{Status: StatusFailed}, lastErr
	}
	return result, nil
}

const maxCoachingRounds = 3

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

	// --- Gateway Evaluation ---
	var plannerInput string

	if input.SkipGateway {
		plannerInput = input.Prompt
	} else if e.Runners.Gateway != nil {
		emit(Event{Type: EventPhaseChange, Phase: PhaseGateway})
		emit(Event{Type: EventAgentStarted, AgentID: "gateway"})
		stream.SetAgent("gateway")

		gw := agent.NewGateway(e.Runners.Gateway, &e.Config.Gateway)
		prompt := input.Prompt

		for round := 0; round < maxCoachingRounds; round++ {
			gwResult, err := gw.Evaluate(ctx, prompt, &streamWriter{buf: stream})
			if err != nil {
				emit(Event{Type: EventAgentFailed, AgentID: "gateway", Err: err})
				emit(Event{Type: EventError, Err: err})
				return
			}

			if gwResult.Verdict == agent.GatewayVerdictAccept {
				emit(Event{Type: EventAgentDone, AgentID: "gateway",
					InputTokens: usageIn(gwResult.Usage), OutputTokens: usageOut(gwResult.Usage)})
				plannerInput = buildPlannerInput(input.Prompt, gwResult.Brief)
				break
			}

			if input.AutoApprove {
				emit(Event{Type: EventAgentDone, AgentID: "gateway",
					InputTokens: usageIn(gwResult.Usage), OutputTokens: usageOut(gwResult.Usage)})
				plannerInput = buildPlannerInput(input.Prompt, gwResult.Brief)
				break
			}

			emit(Event{Type: EventGateRequest, Gate: GateRequest{
				Type:          GateGatewayCoach,
				GatewayResult: gwResult,
			}})

			select {
			case decision := <-decisions:
				switch decision.Type {
				case DecisionCancel:
					emit(Event{Type: EventAgentCancelled, AgentID: "gateway"})
					emit(Event{Type: EventComplete, Phase: PhaseDone})
					return
				case DecisionSkip:
					emit(Event{Type: EventAgentDone, AgentID: "gateway"})
					plannerInput = input.Prompt
					goto research
				case DecisionApprove:
					prompt = incorporateAnswers(prompt, gwResult, decision.GatewayAnswers)
				}
			case <-ctx.Done():
				emit(Event{Type: EventAgentCancelled, AgentID: "gateway"})
				return
			}

			if round == maxCoachingRounds-1 {
				emit(Event{Type: EventAgentDone, AgentID: "gateway",
					InputTokens: usageIn(gwResult.Usage), OutputTokens: usageOut(gwResult.Usage)})
				plannerInput = buildPlannerInput(input.Prompt, gwResult.Brief)
			}
		}
	} else {
		plannerInput = input.Prompt
	}

research:
	var finalPlanMarkdown string

	// If a pre-loaded plan was provided, skip research and planning
	if input.PlanFile != "" {
		finalPlanMarkdown = input.PlanFile
		goto planGate
	}

	// --- Research ---
	{
		emit(Event{Type: EventPhaseChange, Phase: PhaseResearching})
		emit(Event{Type: EventAgentStarted, AgentID: "researcher"})
		stream.SetAgent("researcher")

		researcher := agent.NewResearcher(e.Runners.Researcher, &e.Config.Researcher)
		draft, err := researcher.ResearchStreaming(ctx, plannerInput, &streamWriter{buf: stream})
		if err != nil {
			emit(Event{Type: EventAgentFailed, AgentID: "researcher", Err: err})
			emit(Event{Type: EventError, Err: fmt.Errorf("research: %w", err)})
			return
		}

		writeArtifact(session, "researcher_draft.md", draft.Markdown)
		emit(Event{Type: EventAgentDone, AgentID: "researcher",
			InputTokens: usageIn(draft.Usage), OutputTokens: usageOut(draft.Usage),
			ResearchDraft: draft.Markdown})

		// --- Planning ---
		emit(Event{Type: EventPhaseChange, Phase: PhasePlanning})
		emit(Event{Type: EventAgentStarted, AgentID: "planner"})
		stream.SetAgent("planner")

		planner := agent.NewPlanner(e.Runners.Planner, &e.Config.Planner)
		plan, planErr := planner.RefineStreaming(ctx, draft.Markdown, &streamWriter{buf: stream})
		if planErr != nil {
			emit(Event{Type: EventAgentFailed, AgentID: "planner", Err: planErr})
			emit(Event{Type: EventError, Err: fmt.Errorf("planning: %w", planErr)})
			return
		}

		emit(Event{Type: EventAgentDone, AgentID: "planner",
			InputTokens: usageIn(plan.Usage), OutputTokens: usageOut(plan.Usage)})

		finalPlanMarkdown = plan.Markdown
	}

planGate:
	// --- Plan Approval Gate ---
	emit(Event{Type: EventPlanReady, FinalPlan: finalPlanMarkdown})

	if !input.AutoApprove {
		for {
			emit(Event{Type: EventGateRequest, Gate: GateRequest{
				Type:              GatePlanApproval,
				FinalPlanMarkdown: finalPlanMarkdown,
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
					continue // re-show gate with edited plan
				case DecisionComment:
					// Re-plan with comments
					emit(Event{Type: EventAgentStarted, AgentID: "planner"})
					stream.SetAgent("planner")
					planner := agent.NewPlanner(e.Runners.Planner, &e.Config.Planner)
					revised, err := planner.RefineWithCommentsStreaming(ctx, finalPlanMarkdown, decision.Comment, &streamWriter{buf: stream})
					if err != nil {
						emit(Event{Type: EventAgentFailed, AgentID: "planner", Err: err})
						emit(Event{Type: EventError, Err: fmt.Errorf("planner revision: %w", err)})
						return
					}
					emit(Event{Type: EventAgentDone, AgentID: "planner",
						InputTokens: usageIn(revised.Usage), OutputTokens: usageOut(revised.Usage)})
					finalPlanMarkdown = revised.Markdown
					continue // re-show gate with revised plan
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
		emit(Event{Type: EventComplete, Phase: PhaseDone, FinalPlan: finalPlanMarkdown})
		return
	}

	// --- Worker Execution ---
	emit(Event{Type: EventPhaseChange, Phase: PhaseExecuting})
	emit(Event{Type: EventAgentStarted, AgentID: "worker"})
	stream.SetAgent("worker")

	execPrompt := agent.BuildExecutionPromptFromPlan(finalPlanMarkdown)
	workResult, execErr := e.Runners.Worker.RunStreaming(ctx, execPrompt, "", &streamWriter{buf: stream})
	if execErr != nil {
		emit(Event{Type: EventAgentFailed, AgentID: "worker", Err: execErr})
		emit(Event{Type: EventError, Err: execErr})
		return
	}

	writeArtifact(session, "worker_output.txt", workResult.Output)
	emit(Event{Type: EventAgentDone, AgentID: "worker", WorkOutput: workResult.Output,
		InputTokens: usageIn(workResult.Usage), OutputTokens: usageOut(workResult.Usage)})

	// --- Worker Self-Validation via Session Continuation ---
	emit(Event{Type: EventPhaseChange, Phase: PhaseSelfValidating})
	emit(Event{Type: EventAgentStarted, AgentID: "validator"})
	stream.SetAgent("validator")

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
			emit(Event{Type: EventAgentFailed, AgentID: "validator", Err: valErr})
			// Non-fatal: proceed with whatever output we have
		} else {
			validationOutput = valResult.Output
			emit(Event{Type: EventAgentDone, AgentID: "validator",
				InputTokens: usageIn(valResult.Usage), OutputTokens: usageOut(valResult.Usage)})
		}
	} else {
		slog.Warn("no session ID from worker — attempting disconnected validation")
		// Fallback: run validation as a new session (less effective but still useful)
		valResult, valErr := e.Runners.Worker.RunStreaming(ctx, validationPrompt, "", &streamWriter{buf: stream})
		if valErr != nil {
			slog.Warn("disconnected validation failed", "err", valErr)
			emit(Event{Type: EventAgentFailed, AgentID: "validator", Err: valErr})
		} else {
			validationOutput = valResult.Output
			emit(Event{Type: EventAgentDone, AgentID: "validator",
				InputTokens: usageIn(valResult.Usage), OutputTokens: usageOut(valResult.Usage)})
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
	})
	_ = status // status is captured by Result in Run()
}

// buildPlannerInput constructs the planner prompt from the raw user prompt and
// gateway brief.
func buildPlannerInput(rawPrompt string, brief agent.PromptBrief) string {
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

// incorporateAnswers enriches the prompt with user answers to coaching questions.
func incorporateAnswers(prompt string, gwResult agent.GatewayResult, answers []GatewayAnswer) string {
	var b strings.Builder
	b.WriteString(prompt)
	b.WriteString("\n\nClarifications:\n")
	for _, ans := range answers {
		if ans.QuestionIndex < len(gwResult.Questions) {
			q := gwResult.Questions[ans.QuestionIndex]
			b.WriteString(fmt.Sprintf("- %s: %s\n", q.Text, ans.Answer))
		}
	}
	return b.String()
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

// usageIn extracts InputTokens from a possibly-nil TokenUsage.
func usageIn(u *harness.TokenUsage) int64 {
	if u == nil {
		return 0
	}
	return u.InputTokens
}

// usageOut extracts OutputTokens from a possibly-nil TokenUsage.
func usageOut(u *harness.TokenUsage) int64 {
	if u == nil {
		return 0
	}
	return u.OutputTokens
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
