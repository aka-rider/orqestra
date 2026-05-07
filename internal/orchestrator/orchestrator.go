package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

// EventType classifies orchestrator events emitted to the TUI.
type EventType int

const (
	EventPhaseChange EventType = iota
	EventAgentStarted
	EventAgentDone
	EventAgentFailed
	EventPlanReady
	EventValidationDone
	EventQADone
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

// Event is emitted by the orchestrator to notify the TUI of progress.
type Event struct {
	Type             EventType
	Phase            Phase
	AgentID          string
	AgentStats       *harness.AgentStats
	PlanOutput       *agent.PlanOutput
	ValidationReport *agent.ValidationReport
	QAReport         *agent.ValidationReport
	WorkOutput       string
	Err              error
}

// Input is the user's request to the orchestrator.
type Input struct {
	Prompt             string
	ClarificationInput []string
	SkipExecution      bool
}

// RunStatus classifies the final outcome.
type RunStatus string

const (
	StatusSuccess          RunStatus = "success"
	StatusFailed           RunStatus = "failed"
	StatusAborted          RunStatus = "aborted"
	StatusAcceptedWithWarn RunStatus = "accepted_with_warnings"
)

// Result is the final output of an orchestrator run.
type Result struct {
	Status      RunStatus
	Spec        agent.Specification
	PlanOutput  *agent.PlanOutput
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

// Run executes the full pipeline: gateway → plan → validate → decompose → execute → QA.
func (e *Engine) Run(ctx context.Context, input Input, emit func(Event)) (Result, error) {
	// Create run directory
	var session agent.SessionDir
	if e.RunDirFactory != nil {
		var err error
		session, err = e.RunDirFactory("run")
		if err != nil {
			return Result{}, fmt.Errorf("create run directory: %w", err)
		}
	}

	// --- Gateway Evaluation ---
	emit(Event{Type: EventPhaseChange, Phase: PhaseGateway})

	gw := agent.NewGateway(e.Runners.Gateway, &e.Config.Gateway)
	gwResult, err := gw.Evaluate(ctx, input.Prompt)
	if err != nil {
		emit(Event{Type: EventError, Err: err})
		return Result{Status: StatusFailed}, fmt.Errorf("gateway evaluation: %w", err)
	}

	switch gwResult.Verdict {
	case agent.GatewayVerdictCoach:
		// In interactive mode the TUI handles coaching loops.
		// For the orchestrator, we proceed with the rephrased version.
		if len(input.ClarificationInput) == 0 {
			emit(Event{Type: EventError, Err: fmt.Errorf("coaching needed but no answers provided")})
			return Result{Status: StatusFailed}, fmt.Errorf("coaching needed: %v", gwResult.Questions)
		}
	}

	// --- Planning ---
	emit(Event{Type: EventPhaseChange, Phase: PhasePlanning})

	planner := agent.NewPlanner(e.Runners.Planner, &e.Config.Planner)
	planOutput, err := planner.Plan(ctx, gwResult.Rephrased)
	if err != nil {
		emit(Event{Type: EventError, Err: err})
		return Result{Status: StatusFailed}, fmt.Errorf("planning: %w", err)
	}

	writeArtifact(session, "plan_output.json", planOutput)
	emit(Event{Type: EventPlanReady, PlanOutput: &planOutput})

	spec := planOutput.Spec
	writeArtifact(session, "specification.json", spec)

	// --- Plan Validation ---
	emit(Event{Type: EventPhaseChange, Phase: PhaseValidating})

	validator := agent.NewPlanValidator(e.Runners.Validator, &e.Config.Validator)
	planReport, err := validator.ValidatePlan(ctx, spec)
	if err != nil {
		emit(Event{Type: EventError, Err: err})
		return Result{Status: StatusFailed}, fmt.Errorf("plan validation: %w", err)
	}

	writeArtifact(session, "validation_report.json", planReport)
	emit(Event{Type: EventValidationDone, ValidationReport: planReport})

	if planReport.Verdict == agent.VerdictFail {
		return Result{Status: StatusFailed, Spec: spec, PlanOutput: &planOutput}, nil
	}

	if input.SkipExecution {
		return Result{Status: StatusSuccess, Spec: spec, PlanOutput: &planOutput, RunDir: session.Path}, nil
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

	workOutput, execErr := e.executeWorkerWaves(ctx, spec, projectPlan, emit)
	if execErr != nil {
		emit(Event{Type: EventAgentFailed, AgentID: "worker", Err: execErr})
		return Result{Status: StatusFailed, Spec: spec, PlanOutput: &planOutput, ProjectPlan: projectPlan}, execErr
	}

	// --- QA Gate ---
	emit(Event{Type: EventPhaseChange, Phase: PhaseQA})

	gate := agent.NewGate(e.Runners.QA, &e.Config.QA)
	qaReport, qaErr := gate.ValidateWork(ctx, &agent.QAInput{
		Spec:               spec,
		WorkOutput:         workOutput,
		ValidationCommands: planOutput.ValidationCommands,
		ExpectedArtifacts:  planOutput.ExpectedArtifacts,
	})
	if qaErr != nil {
		emit(Event{Type: EventError, Err: qaErr})
		return Result{Status: StatusFailed, Spec: spec, WorkOutput: workOutput}, qaErr
	}

	writeArtifact(session, "qa_report.json", qaReport)
	emit(Event{Type: EventQADone, QAReport: qaReport})

	// --- Completion ---
	emit(Event{Type: EventPhaseChange, Phase: PhaseDone})

	status := StatusSuccess
	if qaReport.Verdict == agent.VerdictFail {
		status = StatusFailed
	} else if qaReport.Verdict == agent.VerdictWarn {
		status = StatusAcceptedWithWarn
	}

	result := Result{
		Status:      status,
		Spec:        spec,
		PlanOutput:  &planOutput,
		ProjectPlan: projectPlan,
		QAReport:    qaReport,
		WorkOutput:  workOutput,
		RunDir:      session.Path,
	}

	writeArtifact(session, "summary.json", result)
	emit(Event{Type: EventComplete})

	return result, nil
}

// executeWorkerWaves runs work packages in dependency waves.
func (e *Engine) executeWorkerWaves(ctx context.Context, spec agent.Specification, projectPlan *agent.ProjectPlan, emit func(Event)) (string, error) {
	if projectPlan == nil || len(projectPlan.Packages) == 0 {
		// Single execution
		emit(Event{Type: EventAgentStarted, AgentID: "worker"})
		result, err := e.Runners.Worker.RunStreaming(ctx, agent.BuildExecutionPrompt(spec), "", io.Discard)
		if err != nil {
			return "", err
		}
		emit(Event{Type: EventAgentDone, AgentID: "worker", WorkOutput: result.Output})
		return result.Output, nil
	}

	waves := agent.TopoWaves(projectPlan.Packages)
	var allOutput strings.Builder

	for _, wave := range waves {
		type pkgResult struct {
			id     string
			output string
			err    error
		}
		results := make(chan pkgResult, len(wave))
		var wg sync.WaitGroup

		for _, pkg := range wave {
			wg.Add(1)
			go func(wp agent.WorkPackage) {
				defer wg.Done()
				wSpec := wp.ToSpecification(spec)
				emit(Event{Type: EventAgentStarted, AgentID: wp.ID})
				res, err := e.Runners.Worker.RunStreaming(ctx, agent.BuildExecutionPrompt(wSpec), "", io.Discard)
				if err != nil {
					results <- pkgResult{id: wp.ID, err: fmt.Errorf("worker %q: %w", wp.ID, err)}
					return
				}
				emit(Event{Type: EventAgentDone, AgentID: wp.ID, WorkOutput: res.Output})
				results <- pkgResult{id: wp.ID, output: res.Output}
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
