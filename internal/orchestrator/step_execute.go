package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/worktree"
)

// ExecuteStep runs the sandboxed worker agent in an isolated git worktree.
type ExecuteStep struct {
	Spec        harness.ProcessSpec
	Meta        AgentMeta
	RepoPath    string
	// WorktreeSpecFn, when set, builds a spec for the worktree path.
	// If nil, Spec is used directly (no worktree isolation).
	WorktreeSpecFn func(wtPath string) harness.ProcessSpec
	// Supervisor tracks OS resources so they're cleaned up on any exit.
	Sup *Supervisor
}

func (s *ExecuteStep) ID() AgentID { return "worker" }

func (s *ExecuteStep) Run(ctx context.Context, in ExecuteInput, sc StepContext) (ExecuteOutput, error) {
	sc.Obs.AgentStarted(s.ID(), s.Meta)
	start := time.Now()

	// Determine target branch for post-run merge. This is a pre-flight, pre-token
	// boundary (no agent has run yet), so a failure here fails fast rather than
	// silently running the worker against the live, unisolated repo (J8): NEVER
	// treat "branch unknown" as "isolation optional".
	targetBranch, branchErr := worktree.CurrentBranch(ctx, s.RepoPath)
	if branchErr != nil {
		err := fmt.Errorf("determine current branch: %w", branchErr)
		sc.Obs.AgentFailed(s.ID(), err)
		s.writeMeta(sc, "", start, "failed", err, harness.TokenUsage{})
		return ExecuteOutput{}, fmt.Errorf("worker: %w", err)
	}

	// Create isolated worktree when possible.
	var wt worktree.Worktree
	spec := s.Spec

	if s.WorktreeSpecFn != nil && sc.Sessions.Path != "" && targetBranch != "" && in.RunID != "" {
		var wtErr error
		wt, wtErr = worktree.Create(ctx, s.RepoPath, sc.Sessions.Path, in.RunID)
		if wtErr != nil {
			// Isolation was requested (WorktreeSpecFn set) but could not be
			// established. This is an integrity boundary: silently running the
			// worker against the live repo would lose isolation without the
			// pipeline or user ever learning a worktree was skipped (DEFECT-03).
			// Fail closed — surface the failure, never fall back to the direct repo.
			isoErr := fmt.Errorf("worktree isolation requested but creation failed: %w", wtErr)
			sc.Obs.AgentFailed(s.ID(), isoErr)
			s.writeMeta(sc, "", start, "failed", isoErr, harness.TokenUsage{})
			return ExecuteOutput{}, fmt.Errorf("worker: %w", isoErr)
		}
		if s.Sup != nil {
			s.Sup.TrackWorktree(wt)
		}
		spec = s.WorktreeSpecFn(wt.Path)
	}

	execPrompt := agent.BuildExecutionPromptFromPlan(in.FinalPlan)
	spec.Prompt = execPrompt

	sink := SinkFromObserver(s.ID(), sc.Obs)
	res, err := sc.Exec.Run(ctx, spec, nil, sink)

	if err != nil {
		// Preserve the worktree on controlled failure (timeout, loop escalation, etc.)
		// so the partial work can be inspected or resumed. The supervisor would otherwise
		// remove it on Shutdown; untracking here keeps the directory intact.
		if wt.Path != "" && s.Sup != nil {
			s.Sup.UntrackWorktree(wt.Path)
		}
		sc.Obs.AgentFailed(s.ID(), err)
		s.writeMeta(sc, res.SessionID, start, "failed", err, res.Usage)
		return ExecuteOutput{}, fmt.Errorf("worker: %w", err)
	}

	// Prefer the SubmitReport value (tier 1) over the raw output stream (tier 2).
	// The worker runs in input-plane mode (LoopGuard forces it), so the subprocess
	// stays alive after producing its text output; ExpectsReport=true lets the
	// supervisor stop cleanly on SubmitReport and propagates the report via TakeReport.
	workOutput := res.Output
	if sc.Reports != nil {
		if rep, ok := sc.Reports.TakeReport(string(s.ID())); ok && rep != "" {
			workOutput = rep
		}
	}

	sc.Artifacts.WriteBestEffort("worker_output.txt", []byte(workOutput))
	s.writeMeta(sc, res.SessionID, start, "done", nil, res.Usage)
	sc.Obs.AgentDone(s.ID(), res.Usage)

	return ExecuteOutput{
		WorkOutput:   workOutput,
		SessionID:    res.SessionID,
		Worktree:     wt,
		TargetBranch: targetBranch,
		Usage:        res.Usage,
	}, nil
}

func (s *ExecuteStep) writeMeta(sc StepContext, sid string, start time.Time, status string, err error, usage harness.TokenUsage) {
	meta := StepMeta{
		AgentID:              string(s.ID()),
		ModelRef:             s.Meta.ModelRef,
		ModelDisplay:         s.Meta.ModelDisplay,
		Provider:             s.Meta.Provider,
		ContextWindow:        s.Meta.ContextWindow,
		ClaudeSessionID:      sid,
		ClaudeSessionLogPath: resolveSessionLogPath(sc, sid),
		StartTime:            start,
		EndTime:              time.Now(),
		Status:               status,
		InputTokens:          usage.Input,
		OutputTokens:         usage.Output,
	}
	if err != nil {
		meta.Error = err.Error()
	}
	data, jsonErr := json.MarshalIndent(meta, "", "  ")
	if jsonErr != nil {
		return
	}
	sc.Artifacts.WriteBestEffort("worker_meta.json", data)
}
