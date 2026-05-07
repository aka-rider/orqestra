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

// StreamBuffer is a concurrent-safe line buffer shared between the orchestrator
// (writer) and the TUI (reader). The TUI polls it on tick to avoid channel
// backpressure that would block the subprocess.
type StreamBuffer struct {
	mu       sync.Mutex
	lines    []string
	agentID  string
	maxLines int
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
}

// Append adds text to the buffer, accumulating into the current line
// until a newline is encountered. This handles streaming where each
// Write call contains a small text fragment (single token).
func (sb *StreamBuffer) Append(text string) {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	for len(text) > 0 {
		nlIdx := strings.IndexByte(text, '\n')
		if nlIdx == -1 {
			// No newline — append to current (last) line
			if len(sb.lines) == 0 {
				sb.lines = append(sb.lines, text)
			} else {
				sb.lines[len(sb.lines)-1] += text
			}
			break
		}

		// Complete the current line up to the newline
		fragment := text[:nlIdx]
		if len(sb.lines) == 0 {
			sb.lines = append(sb.lines, fragment)
		} else {
			sb.lines[len(sb.lines)-1] += fragment
		}
		// Start a new line for content after the newline
		sb.lines = append(sb.lines, "")
		text = text[nlIdx+1:]
	}

	if len(sb.lines) > sb.maxLines {
		sb.lines = sb.lines[len(sb.lines)-sb.maxLines:]
	}
}

// Snapshot returns the current agent ID and a copy of buffered lines.
func (sb *StreamBuffer) Snapshot() (agentID string, lines []string) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	cp := make([]string, len(sb.lines))
	copy(cp, sb.lines)
	return sb.agentID, cp
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
	EventValidationDone
	EventQADone
	EventGateRequest
	EventComplete
	EventError
)

// Phase represents the current pipeline phase.
type Phase string

const (
	PhaseGateway    Phase = "gateway"
	PhasePlanning   Phase = "planning"
	PhaseValidating Phase = "validating"
	PhaseDecompose  Phase = "decompose"
	PhaseExecuting  Phase = "executing"
	PhaseQA         Phase = "qa"
	PhaseDone       Phase = "done"
)

// GateType identifies which interactive gate the pipeline is waiting at.
type GateType int

const (
	GateGatewayCoach GateType = iota
	GatePlanApproval
)

// GateRequest is emitted when the pipeline needs user input. Passed by value.
type GateRequest struct {
	Type          GateType
	GatewayResult agent.GatewayResult
	PlanOutput    agent.PlanOutput
}

// DecisionType classifies user decisions at gates.
type DecisionType int

const (
	DecisionApprove DecisionType = iota
	DecisionEdit
	DecisionSkip
	DecisionCancel
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
}

// Event is emitted by the orchestrator to notify the TUI of progress.
// All payload fields are values (not pointers) to prevent data races across channels.
type Event struct {
	Type             EventType
	Phase            Phase
	AgentID          string
	PlanOutput       agent.PlanOutput
	ValidationReport agent.ValidationReport
	HasValidation    bool
	QAReport         agent.ValidationReport
	HasQA            bool
	Gate             GateRequest
	WorkOutput       string
	OutputChunk      string // text fragment for EventAgentOutput
	Err              error

	// Token usage from the agent's RunResult. Set on EventAgentDone.
	InputTokens  int64
	OutputTokens int64
}

// Input is the user's request to the orchestrator.
type Input struct {
	Prompt      string
	AutoApprove bool
	SkipGateway bool
}

// RunStatus classifies the final outcome.
type RunStatus string

const (
	StatusSuccess          RunStatus = "success"
	StatusFailed           RunStatus = "failed"
	StatusCancelled        RunStatus = "cancelled"
	StatusAborted          RunStatus = "aborted"
	StatusAcceptedWithWarn RunStatus = "accepted_with_warnings"
)

// Result is the final output of an orchestrator run.
type Result struct {
	Status      RunStatus
	Spec        agent.Specification
	PlanOutput  agent.PlanOutput
	ProjectPlan *agent.ProjectPlan
	QAReport    *agent.ValidationReport
	WorkOutput  string
	RunDir      string
}

// RunDirFactory creates a session directory for artifact persistence.
type RunDirFactory func(slug string) (agent.SessionDir, error)

// Runners holds all CLIRunners for each agent role.
type Runners struct {
	Gateway        harness.CLIRunner
	Planner        harness.CLIRunner
	Validator      harness.CLIRunner
	ProjectManager harness.CLIRunner
	Worker         harness.CLIRunner
	QA             harness.CLIRunner
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
	Stream    *StreamBuffer // shared output buffer, polled by TUI on tick
}

// Start launches the pipeline in a goroutine. Returns channels immediately.
// Pipeline sends events, blocks at gates waiting for decisions.
// The Events channel is closed when the pipeline exits.
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
		AutoApprove: true, // Legacy API auto-approves all gates
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
			var qaPtr *agent.ValidationReport
			if event.HasQA {
				rpt := event.QAReport
				qaPtr = &rpt
			}
			result = Result{
				Status:     StatusSuccess,
				PlanOutput: event.PlanOutput,
				QAReport:   qaPtr,
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
	} else {
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

			// Coach verdict — gate for user input
			if input.AutoApprove {
				// Auto-approve: use current brief as planner input
				emit(Event{Type: EventAgentDone, AgentID: "gateway",
					InputTokens: usageIn(gwResult.Usage), OutputTokens: usageOut(gwResult.Usage)})
				plannerInput = buildPlannerInput(input.Prompt, gwResult.Brief)
				break
			}

			emit(Event{Type: EventGateRequest, Gate: GateRequest{
				Type:          GateGatewayCoach,
				GatewayResult: gwResult,
			}})

			// Block waiting for decision
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
					goto plan
				case DecisionApprove:
					// Incorporate answers and re-evaluate
					prompt = incorporateAnswers(prompt, gwResult, decision.GatewayAnswers)
				}
			case <-ctx.Done():
				emit(Event{Type: EventAgentCancelled, AgentID: "gateway"})
				return
			}

			// If this is the last round, auto-accept
			if round == maxCoachingRounds-1 {
				emit(Event{Type: EventAgentDone, AgentID: "gateway",
					InputTokens: usageIn(gwResult.Usage), OutputTokens: usageOut(gwResult.Usage)})
				plannerInput = buildPlannerInput(input.Prompt, gwResult.Brief)
			}
		}
	}

plan:
	// --- Planning ---
	emit(Event{Type: EventPhaseChange, Phase: PhasePlanning})
	emit(Event{Type: EventAgentStarted, AgentID: "planner"})
	stream.SetAgent("planner")

	planner := agent.NewPlanner(e.Runners.Planner, &e.Config.Planner)
	planOutput, err := planner.PlanStreaming(ctx, plannerInput, &streamWriter{buf: stream})
	if err != nil {
		emit(Event{Type: EventAgentFailed, AgentID: "planner", Err: err})
		emit(Event{Type: EventError, Err: fmt.Errorf("planning: %w", err)})
		return
	}

	writeArtifact(session, "plan_output.json", planOutput)
	emit(Event{Type: EventAgentDone, AgentID: "planner",
		InputTokens: usageIn(planOutput.Usage), OutputTokens: usageOut(planOutput.Usage)})

	// --- Plan Approval Gate ---
	if !input.AutoApprove {
		emit(Event{Type: EventGateRequest, Gate: GateRequest{
			Type:       GatePlanApproval,
			PlanOutput: planOutput,
		}})

		select {
		case decision := <-decisions:
			switch decision.Type {
			case DecisionCancel:
				emit(Event{Type: EventComplete, Phase: PhaseDone})
				return
			case DecisionEdit:
				// Re-parse the edited content as a plan
				edited, parseErr := planner.ParsePlanOutput(decision.EditedContent)
				if parseErr == nil {
					planOutput = edited
				}
			case DecisionApprove:
				// proceed
			}
		case <-ctx.Done():
			return
		}
	}

	spec := planOutput.Spec
	writeArtifact(session, "specification.json", spec)
	emit(Event{Type: EventPlanReady, PlanOutput: planOutput})

	// --- Plan Validation ---
	emit(Event{Type: EventPhaseChange, Phase: PhaseValidating})
	emit(Event{Type: EventAgentStarted, AgentID: "validator"})

	validator := agent.NewPlanValidator(e.Runners.Validator, &e.Config.Validator)
	planReport, err := validator.ValidatePlan(ctx, spec)
	if err != nil {
		emit(Event{Type: EventAgentFailed, AgentID: "validator", Err: err})
		emit(Event{Type: EventError, Err: fmt.Errorf("plan validation: %w", err)})
		return
	}

	writeArtifact(session, "validation_report.json", planReport)
	emit(Event{Type: EventValidationDone, ValidationReport: *planReport, HasValidation: true})
	emit(Event{Type: EventAgentDone, AgentID: "validator",
		InputTokens: usageIn(planReport.Usage), OutputTokens: usageOut(planReport.Usage)})

	if planReport.Verdict == agent.VerdictFail {
		emit(Event{Type: EventError, Err: fmt.Errorf("plan validation failed: %s", planReport.Summary)})
		return
	}

	// --- Project Management Decomposition ---
	emit(Event{Type: EventPhaseChange, Phase: PhaseDecompose})

	var projectPlan *agent.ProjectPlan
	if e.Runners.ProjectManager != nil {
		pm := agent.NewProjectManager(e.Runners.ProjectManager, &e.Config.ProjectManager)
		plan, pmErr := pm.Decompose(ctx, spec)
		if pmErr != nil {
			slog.Warn("project decomposition failed, executing as single package", "err", pmErr)
		} else {
			projectPlan = &plan
			writeArtifact(session, "project_plan.json", plan)
		}
	}

	// --- Worker Execution ---
	emit(Event{Type: EventPhaseChange, Phase: PhaseExecuting})

	workOutput, execErr := e.executeWorkerWaves(ctx, spec, projectPlan, emit, stream)
	if execErr != nil {
		emit(Event{Type: EventAgentFailed, AgentID: "worker", Err: execErr})
		emit(Event{Type: EventError, Err: execErr})
		return
	}

	// --- QA Gate ---
	emit(Event{Type: EventPhaseChange, Phase: PhaseQA})
	emit(Event{Type: EventAgentStarted, AgentID: "qa"})

	gate := agent.NewGate(e.Runners.QA, &e.Config.QA)
	qaReport, qaErr := gate.ValidateWork(ctx, &agent.QAInput{
		Spec:               spec,
		WorkOutput:         workOutput,
		ValidationCommands: planOutput.ValidationCommands,
		ExpectedArtifacts:  planOutput.ExpectedArtifacts,
	})
	if qaErr != nil {
		emit(Event{Type: EventAgentFailed, AgentID: "qa", Err: qaErr})
		emit(Event{Type: EventError, Err: qaErr})
		return
	}

	writeArtifact(session, "qa_report.json", qaReport)
	emit(Event{Type: EventQADone, QAReport: *qaReport, HasQA: true})
	emit(Event{Type: EventAgentDone, AgentID: "qa",
		InputTokens: usageIn(qaReport.Usage), OutputTokens: usageOut(qaReport.Usage)})

	// --- Completion ---
	emit(Event{Type: EventPhaseChange, Phase: PhaseDone})
	emit(Event{Type: EventComplete, Phase: PhaseDone, PlanOutput: planOutput, QAReport: *qaReport, HasQA: true})
}

// buildPlannerInputFromBrief constructs a planner question from a partial brief.
// buildPlannerInput constructs the planner prompt from the raw user prompt and
// gateway brief. The raw prompt is the primary input — the user's original words.
// The brief supplies lightweight structured context (outcome, scope boundaries)
// without replacing or rewriting the user's intent.
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

// executeWorkerWaves runs work packages in dependency waves with config-controlled concurrency.
func (e *Engine) executeWorkerWaves(ctx context.Context, spec agent.Specification, projectPlan *agent.ProjectPlan, emit func(Event), stream *StreamBuffer) (string, error) {
	if projectPlan == nil || len(projectPlan.Packages) == 0 {
		// Single execution
		emit(Event{Type: EventAgentStarted, AgentID: "worker"})
		stream.SetAgent("worker")
		result, err := e.Runners.Worker.RunStreaming(ctx, agent.BuildExecutionPrompt(spec), "", &streamWriter{buf: stream})
		if err != nil {
			return "", err
		}
		emit(Event{Type: EventAgentDone, AgentID: "worker", WorkOutput: result.Output,
			InputTokens: usageIn(result.Usage), OutputTokens: usageOut(result.Usage)})
		return result.Output, nil
	}

	parallelism := e.Config.Worker.Parallelism
	if parallelism <= 0 {
		parallelism = 1
	}

	waves := agent.TopoWaves(projectPlan.Packages)
	var allOutput strings.Builder

	for _, wave := range waves {
		if parallelism == 1 {
			// Sequential — no goroutines
			for _, pkg := range wave {
				wSpec := pkg.ToSpecification(spec)
				emit(Event{Type: EventAgentStarted, AgentID: pkg.ID})
				stream.SetAgent(pkg.ID)
				res, err := e.Runners.Worker.RunStreaming(ctx, agent.BuildExecutionPrompt(wSpec), "", &streamWriter{buf: stream})
				if err != nil {
					return allOutput.String(), fmt.Errorf("worker %q: %w", pkg.ID, err)
				}
				emit(Event{Type: EventAgentDone, AgentID: pkg.ID, WorkOutput: res.Output,
					InputTokens: usageIn(res.Usage), OutputTokens: usageOut(res.Usage)})
				fmt.Fprintf(&allOutput, "=== Package: %s ===\n%s\n", pkg.ID, res.Output)
			}
		} else {
			// Parallel with semaphore
			type pkgResult struct {
				id     string
				output string
				err    error
				usage  *harness.TokenUsage
			}
			sem := make(chan struct{}, parallelism)
			results := make(chan pkgResult, len(wave))
			var wg sync.WaitGroup

			for _, pkg := range wave {
				wg.Add(1)
				go func(wp agent.WorkPackage) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					wSpec := wp.ToSpecification(spec)
					emit(Event{Type: EventAgentStarted, AgentID: wp.ID})
					stream.SetAgent(wp.ID)
					res, err := e.Runners.Worker.RunStreaming(ctx, agent.BuildExecutionPrompt(wSpec), "", &streamWriter{buf: stream})
					if err != nil {
						results <- pkgResult{id: wp.ID, err: fmt.Errorf("worker %q: %w", wp.ID, err)}
						return
					}
					emit(Event{Type: EventAgentDone, AgentID: wp.ID, WorkOutput: res.Output,
						InputTokens: usageIn(res.Usage), OutputTokens: usageOut(res.Usage)})
					results <- pkgResult{id: wp.ID, output: res.Output, usage: res.Usage}
				}(pkg)
			}

			wg.Wait()
			close(results)

			for r := range results {
				if r.err != nil {
					return allOutput.String(), r.err
				}
				fmt.Fprintf(&allOutput, "=== Package: %s ===\n%s\n", r.id, r.output)
			}
		}
	}

	return allOutput.String(), nil
}

// writeArtifact marshals a value to JSON and writes it to the session directory.
func writeArtifact(session agent.SessionDir, name string, v any) {
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

// streamWriter implements io.Writer by appending to a StreamBuffer.
// Unlike channel-based approaches, this never blocks the subprocess.
type streamWriter struct {
	buf *StreamBuffer
}

func (w *streamWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		w.buf.Append(string(p))
	}
	return len(p), nil
}
