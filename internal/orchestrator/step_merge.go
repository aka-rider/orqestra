package orchestrator

import (
	"context"
	"fmt"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/harness"
)

// MergeStep commits the worker's changes and merges the worktree branch into
// the target branch. If no worktree is present (Worktree.Path == ""), it no-ops.
type MergeStep struct {
	// CommitMsgSpec, when set, is used to generate a semantic commit message
	// via session continuation. If nil or if the run fails, a fallback message is used.
	CommitMsgSpec *harness.ProcessSpec
	// Supervisor is used to untrack the worktree after a successful merge.
	Sup *Supervisor
}

func (s *MergeStep) ID() AgentID { return "merger" }

func (s *MergeStep) Run(ctx context.Context, in MergeInput, sc StepContext) (MergeOutput, error) {
	wt := in.Worktree
	if wt.Path == "" {
		// No worktree — nothing to merge.
		return MergeOutput{Status: StatusSuccess}, nil
	}

	// --- Commit message generation (advisory) ---
	semanticMsg := ""
	if s.CommitMsgSpec != nil && in.SessionID != "" {
		spec := *s.CommitMsgSpec
		spec.Prompt = agent.CommitMessagePrompt()
		spec.Resume = harness.ResumeSession(in.SessionID)

		res, err := sc.Exec.Run(ctx, spec, nil, nil)
		if err != nil {
			sc.Log.Warn("commit message generation failed — using fallback", "err", err)
		} else if res.Output != "" {
			parsed, parseErr := agent.ParseCommitMessage(res.Output)
			if parseErr != nil {
				sc.Log.Warn("commit message parse failed — using fallback", "err", parseErr)
			} else {
				semanticMsg = parsed
			}
		}
	}

	buildMsg := func(fallback string) string {
		msg := semanticMsg
		if msg == "" {
			msg = fallback + ": Orqestra automated run"
		}
		if in.RunID != "" {
			msg += "\n\nrun: " + in.RunID + " by Orqestra"
		}
		return msg
	}

	// --- Commit ---
	committed, commitErr := wt.CommitAll(ctx, buildMsg("feat"))
	if commitErr != nil {
		sc.Log.Warn("worktree commit failed — skipping merge", "err", commitErr)
		return MergeOutput{Status: StatusFailed}, nil
	}
	if !committed {
		sc.Log.Info("worktree: nothing to commit — merge skipped")
		return MergeOutput{Status: StatusSuccess}, nil
	}

	// --- Merge ---
	if in.TargetBranch == "" {
		sc.Log.Warn("merge: target branch unknown — merge skipped")
		return MergeOutput{Status: StatusSuccess}, nil
	}

	mergeResult, mergeErr := wt.MergeInto(ctx, in.TargetBranch, buildMsg("merge"))
	if mergeErr != nil {
		sc.Log.Warn("worktree merge failed", "err", mergeErr, "branch", wt.Branch)
		// Emit merge error via obs — best effort.
		return MergeOutput{Status: StatusFailed}, fmt.Errorf("merge into %s: %w", in.TargetBranch, mergeErr)
	}

	if !mergeResult.Merged {
		// Conflicts — surface but don't fail hard; TUI shows merge conflict screen.
		sc.Log.Warn("merge conflict", "branch", wt.Branch, "files", len(mergeResult.ConflictFiles))
		return MergeOutput{Status: StatusFailed}, nil
	}

	// Merge succeeded — clean up worktree and untrack from supervisor.
	if s.Sup != nil {
		s.Sup.UntrackWorktree(wt.Path)
	}
	if rmErr := wt.Remove(context.Background(), true); rmErr != nil {
		sc.Log.Warn("worktree cleanup failed", "err", rmErr)
	}

	return MergeOutput{Status: StatusSuccess}, nil
}
