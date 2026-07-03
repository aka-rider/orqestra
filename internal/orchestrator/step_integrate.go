package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/worktree"
)

// IntegrateStep commits the worker's changes, merges base drift into the
// run branch, fast-forwards the base branch, and cleans up the worktree.
// If no worktree is present (Worktree.Path == ""), it no-ops.
type IntegrateStep struct {
	// CommitMsgSpec is used to generate a semantic commit message from the diff.
	// AgentID must be non-empty for the agent call to fire; falls back on failure.
	CommitMsgSpec harness.ProcessSpec
	// ConflictSpecFn returns a worktree-writable spec for conflict resolution.
	// If nil, conflicts are always surfaced to the user. A returned error means
	// the spec could not be built (e.g. model resolution failure) — handleConflict
	// treats this as give-up-and-preserve, never executes a zero ProcessSpec (J19).
	ConflictSpecFn func(wtPath string) (harness.ProcessSpec, error)
	// Meta is used for agent-started observations.
	Meta AgentMeta
	// Sup is used to untrack the worktree after a successful merge.
	Sup *Supervisor
	// ResolveConflicts controls whether LLM conflict resolution is attempted.
	ResolveConflicts bool
}

func (s *IntegrateStep) ID() AgentID { return "integrator" }

// integratorMeta is written to integrator_meta.json on give-up for recoverability.
type integratorMeta struct {
	BasePreSHA    string   `json:"base_pre_sha,omitempty"`
	ConflictFiles []string `json:"conflict_files,omitempty"`
	GiveUpReason  string   `json:"give_up_reason,omitempty"`
}

func (s *IntegrateStep) Run(ctx context.Context, in IntegrateInput, sc StepContext) (IntegrateOutput, error) {
	wt := in.Worktree
	if wt.Path == "" {
		// No worktree was ever created — worktree isolation was legitimately
		// absent (e.g. WorktreeSpecFn nil). Nothing was isolated, so there is
		// nothing to merge; reporting success here is honest, not a claim of a
		// merge that never happened.
		return IntegrateOutput{Status: StatusSuccess}, nil
	}

	if in.TargetBranch == "" {
		// A worktree WAS created (isolated work exists) but the branch to merge
		// it into is unknown. Reporting success here would be a false claim: the
		// worker's commits stay stranded on the worktree branch with nothing
		// merged into the user's repo (J9). Fail closed instead of no-opping.
		err := fmt.Errorf("integrate: worktree %s present but target branch unknown — refusing to no-op", wt.Path)
		sc.Log.Warn("integrate: target branch unknown", "worktree", wt.Path)
		return IntegrateOutput{Status: StatusFailed}, err
	}

	// Record base pre-SHA for recoverability.
	basePreSHA, shaErr := wt.Head(ctx, in.TargetBranch)
	if shaErr != nil {
		sc.Log.Warn("integrate: could not record base pre-SHA", "err", shaErr)
	}

	// Stage worker's changes.
	staged, stageErr := wt.StageAll(ctx)
	if stageErr != nil {
		return IntegrateOutput{Status: StatusFailed}, fmt.Errorf("integrate: stage: %w", stageErr)
	}

	if staged {
		commitMsg := s.generateCommitMsg(ctx, wt, in, sc)
		if err := wt.CommitStaged(ctx, commitMsg); err != nil {
			return IntegrateOutput{Status: StatusFailed}, fmt.Errorf("integrate: commit: %w", err)
		}
	}

	// Merge base into worktree (handles any drift that accumulated).
	mergeResult, mergeErr := wt.MergeBaseIntoWorktree(ctx, in.TargetBranch)
	if mergeErr != nil {
		return IntegrateOutput{Status: StatusFailed}, fmt.Errorf("integrate: merge base: %w", mergeErr)
	}

	if !mergeResult.Merged {
		return s.handleConflict(ctx, wt, in, sc, mergeResult.ConflictFiles, basePreSHA)
	}

	// Fast-forward base to run branch tip.
	if err := wt.FastForwardBase(ctx, in.TargetBranch); err != nil {
		return IntegrateOutput{Status: StatusFailed}, fmt.Errorf("integrate: fast-forward: %w", err)
	}

	s.cleanup(wt, sc)
	return IntegrateOutput{Status: StatusSuccess}, nil
}

// generateCommitMsg returns a semantic commit message. Falls back to a default
// on any failure (advisory: failure must not block the pipeline).
func (s *IntegrateStep) generateCommitMsg(ctx context.Context, wt worktree.Worktree, in IntegrateInput, sc StepContext) string {
	fallback := s.fallbackMsg(in.RunID)
	if s.CommitMsgSpec.AgentID == "" {
		return fallback
	}

	diff, diffErr := wt.Diff(ctx, in.TargetBranch)
	if diffErr != nil {
		sc.Log.Warn("integrate: diff for commit msg failed — using fallback", "err", diffErr)
		return fallback
	}
	const maxDiffLen = 8192
	if len(diff) > maxDiffLen {
		diff = diff[:maxDiffLen] + "\n... (diff truncated)"
	}

	spec := s.CommitMsgSpec
	spec.Prompt = agent.IntegratorCommitMessagePrompt(diff, extractPlanGoal(in.PlanMarkdown))

	res, err := sc.Exec.Run(ctx, spec, nil, nil)
	if err != nil {
		sc.Log.Warn("integrate: commit msg agent failed — using fallback", "err", err)
		return fallback
	}
	if res.Output != "" {
		if parsed, parseErr := agent.ParseCommitMessage(res.Output); parseErr == nil {
			msg := parsed
			if in.RunID != "" {
				msg += "\n\nrun: " + in.RunID + " by Orqestra"
			}
			return msg
		}
	}
	return fallback
}

// handleConflict attempts resolution (if configured) or surfaces the conflict.
func (s *IntegrateStep) handleConflict(
	ctx context.Context,
	wt worktree.Worktree,
	in IntegrateInput,
	sc StepContext,
	conflictFiles []string,
	basePreSHA string,
) (IntegrateOutput, error) {
	preserve := func(reason string) (IntegrateOutput, error) {
		_ = wt.AbortMergeInWorktree(ctx) // fire-and-forget: best-effort cleanup in failure path
		s.writeMeta(sc, integratorMeta{
			BasePreSHA:    basePreSHA,
			ConflictFiles: conflictFiles,
			GiveUpReason:  reason,
		})
		sc.Log.Warn("integrate: conflict — preserving worktree", "reason", reason, "files", conflictFiles)
		return IntegrateOutput{Status: StatusFailed, ConflictFiles: conflictFiles}, nil
	}

	if !s.ResolveConflicts || s.ConflictSpecFn == nil {
		return preserve("resolve_conflicts=false")
	}

	// Spawn integrator in conflict-resolution mode. A spec-build error is
	// fail-forward truth (J19): give up and preserve the worktree, with the
	// build error carried in the give-up reason — never execute a zero
	// ProcessSpec (no sandbox, no model routing, empty AgentID).
	spec, specErr := s.ConflictSpecFn(wt.Path)
	if specErr != nil {
		return preserve("conflict spec build error: " + specErr.Error())
	}
	spec.Prompt = agent.IntegratorConflictPrompt(conflictFiles)
	res, execErr := sc.Exec.Run(ctx, spec, nil, nil)
	if execErr != nil {
		return preserve("conflict agent error: " + execErr.Error())
	}

	if reason, gaveUp := agent.ParseIntegratorGiveUp(res.Output); gaveUp {
		return preserve("agent gave up: " + reason)
	}

	// Falsifiable checks: markers gone, no unmerged paths, edits confined.
	clean, reason, checkErr := wt.ResolutionClean(ctx, conflictFiles)
	if checkErr != nil {
		return preserve("resolution check error: " + checkErr.Error())
	}
	if !clean {
		return preserve("resolution check failed: " + reason)
	}

	// Stage resolved files and commit with a reviewer-visible note.
	if err := wt.StageFiles(ctx, conflictFiles); err != nil {
		return preserve("stage resolved files: " + err.Error())
	}
	resolveMsg := fmt.Sprintf("Resolve merge conflicts (LLM-assisted)\n\nConflict files: %s\nrun: %s by Orqestra",
		strings.Join(conflictFiles, ", "), in.RunID)
	if err := wt.CommitStaged(ctx, resolveMsg); err != nil {
		return preserve("commit resolved files: " + err.Error())
	}

	// Fast-forward base.
	if err := wt.FastForwardBase(ctx, in.TargetBranch); err != nil {
		return preserve("fast-forward after resolution: " + err.Error())
	}

	s.cleanup(wt, sc)
	return IntegrateOutput{Status: StatusSuccess}, nil
}

// cleanup untracks and removes the worktree after a successful merge.
func (s *IntegrateStep) cleanup(wt worktree.Worktree, sc StepContext) {
	if s.Sup != nil {
		s.Sup.UntrackWorktree(wt.Path)
	}
	if rmErr := wt.Remove(context.Background(), true); rmErr != nil {
		sc.Log.Warn("integrate: worktree cleanup failed", "err", rmErr)
	}
}

func (s *IntegrateStep) fallbackMsg(runID string) string {
	msg := "feat: Orqestra automated run"
	if runID != "" {
		msg += "\n\nrun: " + runID + " by Orqestra"
	}
	return msg
}

func (s *IntegrateStep) writeMeta(sc StepContext, m integratorMeta) {
	data, err := json.Marshal(m)
	if err != nil {
		return // fire-and-forget: advisory artifact
	}
	sc.Artifacts.WriteBestEffort("integrator_meta.json", data)
}

// extractPlanGoal returns the first non-empty content line under "## Goal" in
// plan markdown, or an empty string when not found.
func extractPlanGoal(markdown string) string {
	inGoal := false
	for _, line := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## Goal") {
			inGoal = true
			continue
		}
		if inGoal {
			if strings.HasPrefix(trimmed, "#") {
				break
			}
			if trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}
