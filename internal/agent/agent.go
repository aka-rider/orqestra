package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/planner"
	"github.com/xiii/orqestra/internal/pm"
	"github.com/xiii/orqestra/internal/types"
	"github.com/xiii/orqestra/internal/validator"
)

// Stage represents a pipeline stage.
type Stage string

const (
	StagePlanning          Stage = "planning"
	StagePlanValidation    Stage = "plan_validation"
	StageHumanGate         Stage = "human_gate"
	StageProjectManagement Stage = "project_management"
	StageExecution         Stage = "execution"
	StageWorkValidation    Stage = "work_validation"
	StageComplete          Stage = "complete"
)

// StageEvent is emitted when a pipeline stage changes.
type StageEvent struct {
	Stage Stage
	Err   error
}

// Agent orchestrates the full pipeline: Plan → Validate → Gate → PM → Execute → Validate.
type Agent struct {
	planner        *planner.Planner
	planValidator  *validator.PlanValidator
	workValidator  *validator.WorkValidator
	workerRunner   harness.CLIRunner
	projectManager *pm.ProjectManager
	cfg            *config.Config
	onStage        func(StageEvent)
	// WorkerFactory creates a CLIRunner for a specific work package.
	// If nil, the single workerRunner is used for all packages.
	WorkerFactory func(pkgID string) harness.CLIRunner
}

// NewAgent creates an Agent with all pipeline components wired.
func NewAgent(
	planner *planner.Planner,
	planValidator *validator.PlanValidator,
	workValidator *validator.WorkValidator,
	workerRunner harness.CLIRunner,
	cfg *config.Config,
) *Agent {
	return &Agent{
		planner:       planner,
		planValidator: planValidator,
		workValidator: workValidator,
		workerRunner:  workerRunner,
		cfg:           cfg,
	}
}

// SetProjectManager sets the optional project manager for work decomposition.
func (a *Agent) SetProjectManager(pm *pm.ProjectManager) {
	a.projectManager = pm
}

// OnStage sets a callback for stage transitions.
func (a *Agent) OnStage(fn func(StageEvent)) {
	a.onStage = fn
}

func (a *Agent) emit(stage Stage, err error) {
	if a.onStage != nil {
		a.onStage(StageEvent{Stage: stage, Err: err})
	}
}

// RunResult captures the final pipeline outcome.
type RunResult struct {
	Spec        types.Specification
	ProjectPlan *types.ProjectPlan
	PlanReport  *types.ValidationReport
	WorkOutput  string
	WorkReport  *types.ValidationReport
	Approved    bool
	Stage       Stage
	Err         error
}

// GateFunc is a function that presents the plan and returns approval.
type GateFunc func(spec types.Specification) (bool, error)

// Run executes the full agent pipeline.
// The gate function is called for human approval.
// stdout receives streaming worker output.
func (a *Agent) Run(ctx context.Context, prompt string, gate GateFunc, stdout io.Writer) (*RunResult, error) {
	result := &RunResult{}

	// --- Stage: Planning (with retries) ---
	a.emit(StagePlanning, nil)
	spec, err := a.planWithRetries(ctx, prompt)
	if err != nil {
		result.Stage = StagePlanning
		result.Err = err
		a.emit(StagePlanning, err)
		return result, err
	}
	result.Spec = spec
	slog.Info("planning complete", "goal", spec.Goal, "steps", len(spec.Steps))

	// --- Stage: Plan Validation (with repair loop) ---
	if a.planValidator != nil {
		a.emit(StagePlanValidation, nil)
		report, err := a.validatePlanWithRepair(ctx, &spec, prompt)
		if err != nil {
			result.Stage = StagePlanValidation
			result.Err = err
			a.emit(StagePlanValidation, err)
			return result, err
		}
		result.PlanReport = report
		result.Spec = spec // may have been updated by repair

		if report.Verdict == types.VerdictFail {
			result.Stage = StagePlanValidation
			result.Err = fmt.Errorf("plan validation failed: %s", report.Summary)
			a.emit(StagePlanValidation, result.Err)
			return result, result.Err
		}
		slog.Info("plan validation passed", "verdict", report.Verdict)
	}

	// --- Stage: Human Gate ---
	a.emit(StageHumanGate, nil)
	approved, err := gate(spec)
	if err != nil {
		result.Stage = StageHumanGate
		result.Err = err
		return result, err
	}
	result.Approved = approved
	if !approved {
		result.Stage = StageHumanGate
		return result, nil
	}

	// --- Stage: Project Management (optional) ---
	if a.projectManager != nil {
		a.emit(StageProjectManagement, nil)
		projectPlan, err := a.projectManager.Decompose(ctx, spec)
		if err != nil {
			result.Stage = StageProjectManagement
			result.Err = err
			a.emit(StageProjectManagement, err)
			return result, err
		}
		result.ProjectPlan = &projectPlan
		slog.Info("project management complete", "packages", len(projectPlan.Packages))

		// --- Stage: Multi-Worker Execution ---
		a.emit(StageExecution, nil)
		workOutput, err := a.executePackages(ctx, spec, projectPlan, stdout)
		if err != nil {
			result.Stage = StageExecution
			result.Err = err
			a.emit(StageExecution, err)
			return result, err
		}
		result.WorkOutput = workOutput
		slog.Info("multi-worker execution complete", "packages", len(projectPlan.Packages))
	} else {
		// --- Stage: Execution (single worker, legacy path) ---
		a.emit(StageExecution, nil)
		workOutput, err := a.execute(ctx, spec, stdout)
		if err != nil {
			result.Stage = StageExecution
			result.Err = err
			a.emit(StageExecution, err)
			return result, err
		}
		result.WorkOutput = workOutput
		slog.Info("execution complete")
	}

	// --- Stage: Work Validation (with repair loop) ---
	if a.workValidator != nil {
		a.emit(StageWorkValidation, nil)
		workReport, err := a.validateWorkWithRepair(ctx, spec, result.WorkOutput, stdout)
		if err != nil {
			result.Stage = StageWorkValidation
			result.Err = err
			a.emit(StageWorkValidation, err)
			return result, err
		}
		result.WorkReport = workReport

		if workReport.Verdict == types.VerdictFail {
			result.Stage = StageWorkValidation
			result.Err = fmt.Errorf("work validation failed: %s", workReport.Summary)
			a.emit(StageWorkValidation, result.Err)
			return result, result.Err
		}
		slog.Info("work validation passed", "verdict", workReport.Verdict)
	}

	// --- Stage: Complete ---
	result.Stage = StageComplete
	a.emit(StageComplete, nil)
	return result, nil
}

// RunFromSpec runs the pipeline starting from an already-constructed Specification,
// skipping the planning phase. It proceeds through:
// Plan Validation → Human Gate → Execution → Work Validation.
func (a *Agent) RunFromSpec(ctx context.Context, spec types.Specification, gate GateFunc, stdout io.Writer) (*RunResult, error) {
	result := &RunResult{Spec: spec}

	// --- Stage: Plan Validation ---
	if a.planValidator != nil {
		a.emit(StagePlanValidation, nil)
		report, err := a.planValidator.Validate(ctx, spec)
		if err != nil {
			result.Stage = StagePlanValidation
			result.Err = err
			a.emit(StagePlanValidation, err)
			return result, err
		}
		result.PlanReport = report
		if report.Verdict == types.VerdictFail {
			result.Stage = StagePlanValidation
			result.Err = fmt.Errorf("plan validation failed: %s", report.Summary)
			a.emit(StagePlanValidation, result.Err)
			return result, result.Err
		}
		slog.Info("plan validation passed", "verdict", report.Verdict)
	}

	// --- Stage: Human Gate ---
	a.emit(StageHumanGate, nil)
	approved, err := gate(spec)
	if err != nil {
		result.Stage = StageHumanGate
		result.Err = err
		return result, err
	}
	result.Approved = approved
	if !approved {
		result.Stage = StageHumanGate
		return result, nil
	}

	// --- Stage: Project Management (optional) ---
	if a.projectManager != nil {
		a.emit(StageProjectManagement, nil)
		projectPlan, err := a.projectManager.Decompose(ctx, spec)
		if err != nil {
			result.Stage = StageProjectManagement
			result.Err = err
			a.emit(StageProjectManagement, err)
			return result, err
		}
		result.ProjectPlan = &projectPlan
		slog.Info("project management complete", "packages", len(projectPlan.Packages))

		a.emit(StageExecution, nil)
		workOutput, err := a.executePackages(ctx, spec, projectPlan, stdout)
		if err != nil {
			result.Stage = StageExecution
			result.Err = err
			a.emit(StageExecution, err)
			return result, err
		}
		result.WorkOutput = workOutput
	} else {
		// --- Stage: Execution (single worker) ---
		a.emit(StageExecution, nil)
		workOutput, err := a.execute(ctx, spec, stdout)
		if err != nil {
			result.Stage = StageExecution
			result.Err = err
			a.emit(StageExecution, err)
			return result, err
		}
		result.WorkOutput = workOutput
		slog.Info("execution complete")
	}

	// --- Stage: Work Validation ---
	if a.workValidator != nil {
		a.emit(StageWorkValidation, nil)
		workReport, err := a.validateWorkWithRepair(ctx, spec, result.WorkOutput, stdout)
		if err != nil {
			result.Stage = StageWorkValidation
			result.Err = err
			a.emit(StageWorkValidation, err)
			return result, err
		}
		result.WorkReport = workReport
		if workReport.Verdict == types.VerdictFail {
			result.Stage = StageWorkValidation
			result.Err = fmt.Errorf("work validation failed: %s", workReport.Summary)
			a.emit(StageWorkValidation, result.Err)
			return result, result.Err
		}
		slog.Info("work validation passed", "verdict", workReport.Verdict)
	}

	result.Stage = StageComplete
	a.emit(StageComplete, nil)
	return result, nil
}

// planWithRetries attempts planning with the configured retry budget.
func (a *Agent) planWithRetries(ctx context.Context, prompt string) (types.Specification, error) {
	attempts := a.cfg.Retry.PlannerAttempts
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for i := 0; i < attempts; i++ {
		spec, err := a.planner.Plan(ctx, prompt)
		if err != nil {
			lastErr = err
			slog.Warn("planner attempt failed", "attempt", i+1, "err", err)
			continue
		}
		return spec, nil
	}
	return types.Specification{}, fmt.Errorf("planner exhausted %d attempts: %w", attempts, lastErr)
}

// validatePlanWithRepair validates and optionally re-plans on failure.
func (a *Agent) validatePlanWithRepair(ctx context.Context, spec *types.Specification, prompt string) (*types.ValidationReport, error) {
	repairs := a.cfg.Retry.PlanValidationRepair
	if repairs < 0 {
		repairs = 0
	}

	for i := 0; i <= repairs; i++ {
		report, err := a.planValidator.Validate(ctx, *spec)
		if err != nil {
			return nil, err
		}

		if report.Verdict != types.VerdictFail {
			return report, nil
		}

		if i < repairs {
			// Re-plan with feedback
			slog.Info("plan validation failed, re-planning", "attempt", i+1, "summary", report.Summary)
			feedback := formatValidationFeedback(report)
			repairPrompt := fmt.Sprintf("%s\n\nPrevious plan was rejected by validator:\n%s\nPlease fix the issues and produce a corrected specification.", prompt, feedback)

			newSpec, err := a.planWithRetries(ctx, repairPrompt)
			if err != nil {
				return report, nil // return the failed report if re-planning fails
			}
			*spec = newSpec
		} else {
			return report, nil
		}
	}

	return nil, fmt.Errorf("plan validation repair exhausted")
}

// execute runs the worker harness and captures output.
func (a *Agent) execute(ctx context.Context, spec types.Specification, stdout io.Writer) (string, error) {
	execPrompt := buildExecutionPrompt(spec)
	result, err := a.workerRunner.RunStreaming(ctx, execPrompt, "", stdout)
	if err != nil {
		return "", err
	}
	return result.Output, nil
}

// validateWorkWithRepair validates work and optionally re-executes.
func (a *Agent) validateWorkWithRepair(ctx context.Context, spec types.Specification, workOutput string, stdout io.Writer) (*types.ValidationReport, error) {
	repairs := a.cfg.Retry.WorkValidationRepair
	if repairs < 0 {
		repairs = 0
	}

	currentOutput := workOutput
	for i := 0; i <= repairs; i++ {
		report, err := a.workValidator.Validate(ctx, &validator.WorkValidationInput{
			Spec:       spec,
			WorkOutput: currentOutput,
		})
		if err != nil {
			return nil, err
		}

		if report.Verdict != types.VerdictFail {
			return report, nil
		}

		if i < repairs {
			slog.Info("work validation failed, re-executing", "attempt", i+1, "summary", report.Summary)
			feedback := formatValidationFeedback(report)
			repairPrompt := fmt.Sprintf("The previous execution was rejected by the validator:\n%s\n\nOriginal spec goal: %s\nPlease fix the issues.", feedback, spec.Goal)
			result, err := a.workerRunner.RunStreaming(ctx, repairPrompt, "", stdout)
			if err != nil {
				return report, nil
			}
			currentOutput = result.Output
		} else {
			return report, nil
		}
	}

	return nil, fmt.Errorf("work validation repair exhausted")
}

// buildExecutionPrompt renders the specification into a worker prompt.
func buildExecutionPrompt(spec types.Specification) string {
	prompt := fmt.Sprintf("Execute the following plan:\n\nGoal: %s\n\nSteps:\n", spec.Goal)
	for i, step := range spec.Steps {
		prompt += fmt.Sprintf("%d. %s\n", i+1, step)
	}
	if len(spec.Acceptance) > 0 {
		prompt += "\nAcceptance Criteria:\n"
		for _, criterion := range spec.Acceptance {
			prompt += fmt.Sprintf("- %s\n", criterion)
		}
	}
	return prompt
}

// formatValidationFeedback renders a validation report into text feedback for re-planning.
func formatValidationFeedback(report *types.ValidationReport) string {
	result := fmt.Sprintf("Verdict: %s\nSummary: %s\n", report.Verdict, report.Summary)
	if len(report.Issues) > 0 {
		result += "Issues:\n"
		for _, issue := range report.Issues {
			result += fmt.Sprintf("  [%s] %s: %s\n", issue.Severity, issue.ID, issue.Message)
		}
	}
	if len(report.Suggestions) > 0 {
		result += "Suggestions:\n"
		for _, s := range report.Suggestions {
			result += fmt.Sprintf("  - %s\n", s)
		}
	}
	return result
}

// executePackages runs work packages respecting dependency order.
// Packages in the same wave (no mutual dependencies) execute in parallel.
func (a *Agent) executePackages(ctx context.Context, parentSpec types.Specification, plan types.ProjectPlan, stdout io.Writer) (string, error) {
	waves := topoWaves(plan.Packages)

	var allOutput strings.Builder
	for waveIdx, wave := range waves {
		slog.Info("executing wave", "wave", waveIdx+1, "packages", len(wave))

		type pkgResult struct {
			id     string
			output string
			err    error
		}

		results := make(chan pkgResult, len(wave))
		var wg sync.WaitGroup

		for _, pkg := range wave {
			wg.Add(1)
			go func(wp types.WorkPackage) {
				defer wg.Done()

				runner := a.workerRunner
				if a.WorkerFactory != nil {
					runner = a.WorkerFactory(wp.ID)
				}

				spec := wp.ToSpecification(parentSpec)
				prompt := buildExecutionPrompt(spec)
				slog.Info("worker started", "package", wp.ID)

				res, err := runner.RunStreaming(ctx, prompt, "", stdout)
				if err != nil {
					results <- pkgResult{id: wp.ID, err: fmt.Errorf("worker %q: %w", wp.ID, err)}
					return
				}
				slog.Info("worker done", "package", wp.ID)
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

// topoWaves sorts work packages into dependency waves using Kahn's algorithm.
// Each wave contains packages whose dependencies are all in prior waves.
func topoWaves(packages []types.WorkPackage) [][]types.WorkPackage {
	idx := make(map[string]int, len(packages))
	for i, pkg := range packages {
		idx[pkg.ID] = i
	}

	inDegree := make([]int, len(packages))
	for i, pkg := range packages {
		_ = i
		for range pkg.DependsOn {
			inDegree[i]++
		}
	}

	var queue []int
	for i, d := range inDegree {
		if d == 0 {
			queue = append(queue, i)
		}
	}

	var waves [][]types.WorkPackage
	for len(queue) > 0 {
		wave := make([]types.WorkPackage, len(queue))
		for i, qi := range queue {
			wave[i] = packages[qi]
		}
		waves = append(waves, wave)

		var nextQueue []int
		for _, qi := range queue {
			curID := packages[qi].ID
			for i, pkg := range packages {
				for _, dep := range pkg.DependsOn {
					if dep == curID {
						inDegree[i]--
						if inDegree[i] == 0 {
							nextQueue = append(nextQueue, i)
						}
					}
				}
			}
		}
		queue = nextQueue
	}

	return waves
}
