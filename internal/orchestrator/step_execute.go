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

	// Determine target branch for post-run merge.
	targetBranch, branchErr := worktree.CurrentBranch(ctx, s.RepoPath)
	if branchErr != nil {
		sc.Log.Warn("cannot determine current branch — worktree isolation disabled", "err", branchErr)
	}

	// Create isolated worktree when possible.
	var wt worktree.Worktree
	spec := s.Spec

	start := time.Now()

	if s.WorktreeSpecFn != nil && sc.Sessions.Path != "" && targetBranch != "" && in.RunID != "" {
		var wtErr error
		wt, wtErr = worktree.Create(ctx, s.RepoPath, sc.Sessions.Path, in.RunID)
		if wtErr != nil {
			sc.Log.Warn("worktree creation failed — falling back to direct repo", "err", wtErr)
			wt = worktree.Worktree{}
		} else {
			if s.Sup != nil {
				s.Sup.TrackWorktree(wt)
			}
			spec = s.WorktreeSpecFn(wt.Path)
		}
	}

	execPrompt := agent.BuildExecutionPromptFromPlan(in.FinalPlan)
	spec.Prompt = execPrompt

	sink := SinkFromObserver(s.ID(), sc.Obs)
	res, err := sc.Exec.Run(ctx, spec, nil, sink)

	if err != nil {
		sc.Obs.AgentFailed(s.ID(), err)
		s.writeMeta(sc, res.SessionID, start, "failed", err, harness.TokenUsage{})
		return ExecuteOutput{}, fmt.Errorf("worker: %w", err)
	}

	sc.Artifacts.WriteBestEffort("worker_output.txt", []byte(res.Output))
	s.writeMeta(sc, res.SessionID, start, "done", nil, res.Usage)
	sc.Obs.AgentDone(s.ID(), res.Usage)

	return ExecuteOutput{
		WorkOutput:   res.Output,
		SessionID:    res.SessionID,
		Worktree:     wt,
		TargetBranch: targetBranch,
		Usage:        res.Usage,
	}, nil
}

func (s *ExecuteStep) writeMeta(sc StepContext, sid string, start time.Time, status string, err error, usage harness.TokenUsage) {
	meta := StepMeta{
		AgentID:         string(s.ID()),
		ModelRef:        s.Meta.ModelRef,
		ModelDisplay:    s.Meta.ModelDisplay,
		Provider:        s.Meta.Provider,
		ContextWindow:   s.Meta.ContextWindow,
		ClaudeSessionID: sid,
		StartTime:       start,
		EndTime:         time.Now(),
		Status:          status,
		InputTokens:     usage.Input,
		OutputTokens:    usage.Output,
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
