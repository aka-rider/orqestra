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
	// PlanFilePath is the last known on-disk plan file path for SessionID —
	// execution metadata (§1.8, same justification as SessionID) carried
	// forward so a later Revise call can snapshot the plan file's
	// PRE-invocation state (WP11/J35: tier 2 of the report harvester must
	// fire only when the plan file changed during the invocation it is
	// being read for, not whenever it merely exists).
	PlanFilePath string
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
	Revise     Step[ReviseInput, PlanOutput] // used inside gate loop
	Execute    Step[ExecuteInput, ExecuteOutput]
	Validate   Step[ValidateInput, ValidateOutput]   // nil = skip
	Integrate  Step[IntegrateInput, IntegrateOutput] // nil = skip
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
			dec, gateErr := sc.Gate(ctx, GateRequest{
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
			if dec.Type == DecisionEdit && dec.AutoApprove && dec.Comment == "" {
				// Confirmed edit (J26): the user already reviewed and confirmed
				// this exact markdown (^E → save → Yes) — treat it as final
				// approval. No architect re-engagement, no second gate cycle.
				// An empty EditedContent means "confirm as-is": keep the current plan.
				if dec.EditedContent != "" {
					plan.Markdown = dec.EditedContent
				}
				break
			}
			// Comment or a non-auto-approved Edit: revise the plan and re-open the gate.
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
	// Validation is advisory (fail-forward zone): a FAIL verdict never fails the
	// pipeline by default. But the verdict is no longer discarded (J33) — it
	// is threaded into Result below, and the optional
	// BlockMergeOnValidationFail gate (J33/WP8) reads it to decide whether to
	// skip Integrate.
	var valOutput string
	var valVerdict agent.Verdict
	if setup.Validation && steps.Validate != nil {
		sc.Obs.PhaseChanged(PhaseSelfValidating)
		val, _ := steps.Validate.Run(ctx, ValidateInput{ // fire-and-forget: validation is advisory; failure does not block merge
			WorkerSessionID: exec.SessionID,
			FinalPlan:       plan.Markdown,
		}, sc)
		valOutput = val.Output
		valVerdict = val.Parsed.Verdict
	}

	// --- Optional gate: block merge on a FAIL verdict (J33/WP8) ---
	// Default false = today's behavior (Integrate always runs), now honest:
	// the verdict is visible in Result either way. When enabled and the
	// worker self-reported FAILED, refuse to merge — the worktree is left
	// exactly as Execute produced it (Integrate never runs, so nothing is
	// committed, merged, or removed) so the user's base and the work stay
	// fully recoverable (CLAUDE.md §0).
	if setup.BlockMergeOnValidationFail && valVerdict == agent.VerdictFail {
		return Result{
				Status:            StatusFailed,
				FinalPlan:         plan.Markdown,
				WorkerValidation:  valOutput,
				ValidationVerdict: valVerdict,
			}, fmt.Errorf("block_merge_on_validation_fail: worker self-validation verdict is FAIL — refusing to merge, worktree preserved at %q (branch %q)",
				exec.Worktree.Path, exec.TargetBranch)
	}

	// --- Integrate (commit + merge) ---
	integrateStatus := StatusSuccess
	var conflictFiles []string
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
		conflictFiles = intResult.ConflictFiles
	}

	return Result{
		Status:            integrateStatus,
		FinalPlan:         plan.Markdown,
		WorkerValidation:  valOutput,
		ValidationVerdict: valVerdict,
		ConflictFiles:     conflictFiles,
	}, nil
}
