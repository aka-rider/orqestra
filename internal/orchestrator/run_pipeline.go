package orchestrator

import (
	"context"
	"fmt"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/worktree"
)

// --- Step input/output types ---

// PipelineRunInput is the caller's request to RunPipeline.
type PipelineRunInput struct {
	Prompt string
	RunID  string // unique ID for this run; used in worktree commit messages
}

// PlanOutput is the shared type returned by both Deliberate and Revise steps.
type PlanOutput struct {
	Markdown  string
	Warnings  []string
	SessionID string
}

// DeliberateInput is the input to the deliberation step.
type DeliberateInput struct {
	OriginalPrompt string
}

// ReviseInput is the input to a gate-loop revision turn.
type ReviseInput struct {
	Plan     PlanOutput
	Decision Decision
}

// ExecuteInput is the input to the worker execution step.
type ExecuteInput struct {
	FinalPlan string
	RunID     string
}

// ExecuteOutput is the output of the worker execution step.
type ExecuteOutput struct {
	WorkOutput   string
	SessionID    string
	Worktree     worktree.Worktree
	TargetBranch string // branch to merge into (populated by ExecuteStep)
	Usage        harness.TokenUsage
}

// ValidateInput is the input to the worker self-validation step.
type ValidateInput struct {
	WorkerSessionID string
	FinalPlan       string
}

// ValidateOutput is the output of the worker self-validation step.
type ValidateOutput struct {
	Output string
	Parsed agent.ValidationOutput
}

// IntegrateInput is the input to the worktree integrate step.
type IntegrateInput struct {
	Worktree     worktree.Worktree
	RunID        string
	PlanMarkdown string // final approved plan; used to extract a goal for the commit message
	TargetBranch string
}

// IntegrateOutput is the output of the integrate step.
type IntegrateOutput struct {
	Status        RunStatus
	ConflictFiles []string // populated on conflict give-up
}

// --- Steps container ---

// PipelineSteps holds the concrete step implementations for RunPipeline.
// Validate and Integrate are optional (nil = skip).
type PipelineSteps struct {
	Deliberate Step[DeliberateInput, PlanOutput]
	Revise     Step[ReviseInput, PlanOutput]       // used inside gate loop
	Execute    Step[ExecuteInput, ExecuteOutput]
	Validate   Step[ValidateInput, ValidateOutput]    // nil = skip
	Integrate  Step[IntegrateInput, IntegrateOutput]  // nil = skip
}

// --- RunPipeline ---

// RunPipeline executes the full orchestration pipeline as literal Go control flow.
// Each Out feeds the next In; if err != nil, return immediately — no EventComplete scraping.
// The gate loop is a literal for{} loop: comment→revise→re-open, break on approve.
func RunPipeline(ctx context.Context, setup PipelineSetup, in PipelineRunInput,
	sc StepContext, steps PipelineSteps) (Result, error) {

	// --- Deliberation (architect + critic) ---
	// There is no standalone research stage: the architect researches on demand via the
	// orqestra-researcher subagent, starting from the raw user prompt.
	sc.Obs.PhaseChanged(PhasePlanning)
	plan, err := steps.Deliberate.Run(ctx, DeliberateInput{
		OriginalPrompt: in.Prompt,
	}, sc)
	if err != nil {
		return Result{Status: StatusFailed}, fmt.Errorf("deliberate: %w", err)
	}

	// --- Gate after deliberation (plan review loop) ---
	if setup.HumanGates.Active(GateAfterDeliberation) {
		for {
			dec, gateErr := sc.Control.Gate(ctx, GateRequest{
				Position:          GateAfterDeliberation,
				FinalPlanMarkdown: plan.Markdown,
				PlanWarnings:      plan.Warnings,
			})
			if gateErr != nil {
				return Result{Status: StatusFailed}, fmt.Errorf("gate-after-deliberation: %w", gateErr)
			}
			if dec.Type == DecisionCancel {
				return Result{Status: StatusCancelled}, nil
			}
			if dec.Type == DecisionApprove {
				break
			}
			// Comment or Edit: revise the plan and re-open the gate.
			revised, revErr := steps.Revise.Run(ctx, ReviseInput{Plan: plan, Decision: dec}, sc)
			if revErr != nil {
				return Result{Status: StatusFailed}, fmt.Errorf("revise: %w", revErr)
			}
			plan = revised
		}
	}

	// Approved-plan write is fail-closed (integrity boundary).
	if err := sc.Artifacts.Write("final_plan.md", []byte(plan.Markdown)); err != nil {
		return Result{Status: StatusFailed}, fmt.Errorf("write final_plan.md: %w", err)
	}

	if !setup.Execution {
		return Result{Status: StatusSuccess, FinalPlan: plan.Markdown}, nil
	}

	// --- Execution ---
	sc.Obs.PhaseChanged(PhaseExecuting)
	exec, err := steps.Execute.Run(ctx, ExecuteInput{
		FinalPlan: plan.Markdown,
		RunID:     in.RunID,
	}, sc)
	if err != nil {
		return Result{Status: StatusFailed}, fmt.Errorf("execute: %w", err)
	}

	// --- Validation (advisory) ---
	var valOutput string
	if setup.Validation && steps.Validate != nil {
		sc.Obs.PhaseChanged(PhaseSelfValidating)
		val, _ := steps.Validate.Run(ctx, ValidateInput{ // fire-and-forget: validation is advisory; failure does not block merge
			WorkerSessionID: exec.SessionID,
			FinalPlan:       plan.Markdown,
		}, sc)
		valOutput = val.Output
	}

	// --- Integrate (commit + merge) ---
	integrateStatus := StatusSuccess
	if steps.Integrate != nil {
		intResult, intErr := steps.Integrate.Run(ctx, IntegrateInput{
			Worktree:     exec.Worktree,
			RunID:        in.RunID,
			PlanMarkdown: plan.Markdown,
			TargetBranch: exec.TargetBranch,
		}, sc)
		if intErr != nil {
			return Result{Status: StatusFailed}, fmt.Errorf("integrate: %w", intErr)
		}
		integrateStatus = intResult.Status
	}

	return Result{
		Status:           integrateStatus,
		FinalPlan:        plan.Markdown,
		WorkerValidation: valOutput,
	}, nil
}
