package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/llm"
	"github.com/xiii/orqestra/internal/planner"
	"github.com/xiii/orqestra/internal/types"
	"github.com/xiii/orqestra/internal/validator"
)

// Stage represents a pipeline stage.
type Stage string

const (
	StagePlanning       Stage = "planning"
	StagePlanValidation Stage = "plan_validation"
	StageHumanGate      Stage = "human_gate"
	StageExecution      Stage = "execution"
	StageWorkValidation Stage = "work_validation"
	StageComplete       Stage = "complete"
)

// StageEvent is emitted when a pipeline stage changes.
type StageEvent struct {
	Stage Stage
	Err   error
}

// Agent orchestrates the full pipeline: Plan → Validate → Gate → Execute → Validate.
type Agent struct {
	planner       *planner.Planner
	planValidator *validator.PlanValidator
	workValidator *validator.WorkValidator
	workerClient  *harness.Client
	cfg           *config.Config
	onStage       func(StageEvent)
}

// NewAgent creates an Agent with all pipeline components wired.
func NewAgent(
	planner *planner.Planner,
	planValidator *validator.PlanValidator,
	workValidator *validator.WorkValidator,
	workerClient *harness.Client,
	cfg *config.Config,
) *Agent {
	return &Agent{
		planner:       planner,
		planValidator: planValidator,
		workValidator: workValidator,
		workerClient:  workerClient,
		cfg:           cfg,
	}
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
	Spec       types.Specification
	PlanReport *types.ValidationReport
	WorkOutput string
	WorkReport *types.ValidationReport
	Approved   bool
	Stage      Stage
	Err        error
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

	// --- Stage: Execution ---
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

	// --- Stage: Work Validation (with repair loop) ---
	if a.workValidator != nil {
		a.emit(StageWorkValidation, nil)
		workReport, err := a.validateWorkWithRepair(ctx, spec, workOutput, stdout)
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

// planWithRetries attempts planning with the configured retry budget.
func (a *Agent) planWithRetries(ctx context.Context, prompt string) (types.Specification, error) {
	attempts := a.cfg.Retry.PlannerAttempts
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for i := 0; i < attempts; i++ {
		result, err := a.planner.Plan(ctx, prompt)
		if err != nil {
			lastErr = err
			slog.Warn("planner attempt failed", "attempt", i+1, "err", err)
			continue
		}
		if result.IsOk() {
			return result.Value, nil
		}
		lastErr = result.Err
		slog.Warn("planner produced invalid spec", "attempt", i+1, "err", result.Err)
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
	resp, err := a.workerClient.RunStreaming(ctx, execPrompt, "", stdout)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
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
			resp, err := a.workerClient.RunStreaming(ctx, repairPrompt, "", stdout)
			if err != nil {
				return report, nil
			}
			currentOutput = resp.Content
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

// NewFromConfig creates a fully-wired Agent from the application config.
// If validator endpoints are unreachable, validators will be nil (skipped).
func NewFromConfig(cfg *config.Config) *Agent {
	// Planner client
	planClient := harness.NewClient(cfg.Planner.Model, cfg.Planner.AllowedTools)

	// Worker client
	workerClient := harness.NewClient(cfg.Worker.Model, cfg.Worker.AllowedTools)
	workerClient.PermissionMode = cfg.Worker.PermissionMode

	// Planner
	p := planner.New(planClient, &cfg.Planner)

	// Plan Validator (via LLM provider)
	var pv *validator.PlanValidator
	if cfg.Validator.BaseURL != "" {
		provider := llm.NewOpenAIProvider(
			"plan-validator",
			cfg.Validator.BaseURL,
			cfg.Validator.Model,
			cfg.Validator.APIKey,
		)
		pv = validator.NewPlanValidator(provider, &cfg.Validator)
	}

	// Work Validator (via LLM provider)
	var wv *validator.WorkValidator
	if cfg.WorkValidator.BaseURL != "" {
		provider := llm.NewOpenAIProvider(
			"work-validator",
			cfg.WorkValidator.BaseURL,
			cfg.WorkValidator.Model,
			cfg.WorkValidator.APIKey,
		)
		wv = validator.NewWorkValidator(provider, &cfg.WorkValidator)
	}

	return NewAgent(p, pv, wv, workerClient, cfg)
}

// Ensure JSON is imported (used in formatValidationFeedback context).
var _ = json.Marshal
