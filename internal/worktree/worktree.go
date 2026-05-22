package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Worktree manages the lifecycle of a git worktree created for a single pipeline run.
type Worktree struct {
	RepoPath string // absolute path to the main repository root
	Path     string // absolute path to the worktree directory
	Branch   string // name of the worktree branch (e.g. "orqestra-run-<id>")
}

// Create creates a git worktree at <sessionDir>/worktree on a new branch.
// The worktree is based on HEAD of the main repo.
// Returns an error if git is unavailable or the repo is not a git repository.
func Create(ctx context.Context, repoPath, sessionDir, runID string) (Worktree, error) {
	branch := "orqestra-run-" + runID
	wtPath := filepath.Join(sessionDir, "worktree")

	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return Worktree{}, fmt.Errorf("worktree: create session dir: %w", err)
	}

	cmd := exec.CommandContext(ctx, "git", "worktree", "add", "-b", branch, wtPath)
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return Worktree{}, fmt.Errorf("worktree: git worktree add: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}

	return Worktree{RepoPath: repoPath, Path: wtPath, Branch: branch}, nil
}

// Remove removes the worktree directory and its branch. The force flag is needed
// when the worktree has uncommitted changes (e.g. after a cancelled run).
func (w Worktree) Remove(ctx context.Context, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, w.Path)

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = w.RepoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("worktree: git worktree remove %q: %w (output: %s)", w.Path, err, strings.TrimSpace(string(out)))
	}

	// Delete the branch that was created for the worktree.
	delCmd := exec.CommandContext(ctx, "git", "branch", "-D", w.Branch)
	delCmd.Dir = w.RepoPath
	if out, err := delCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("worktree: delete branch %q: %w (output: %s)", w.Branch, err, strings.TrimSpace(string(out)))
	}

	return nil
}

// RemoveDir removes only the worktree directory without deleting the branch.
// Used when merge fails and the branch should be preserved for manual resolution.
func (w Worktree) RemoveDir(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "git", "worktree", "remove", "--force", w.Path)
	cmd.Dir = w.RepoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("worktree: remove dir %s: %w (output: %s)", w.Path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CommitAll stages all changes in the worktree and creates a commit.
// Returns (false, nil) when there is nothing to commit (clean working tree).
func (w Worktree) CommitAll(ctx context.Context, message string) (committed bool, err error) {
	// Check for changes first
	statusCmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	statusCmd.Dir = w.Path
	out, err := statusCmd.Output()
	if err != nil {
		return false, fmt.Errorf("worktree: git status: %w", err)
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return false, nil // nothing to commit
	}

	addCmd := exec.CommandContext(ctx, "git", "add", "-A")
	addCmd.Dir = w.Path
	if addOut, addErr := addCmd.CombinedOutput(); addErr != nil {
		return false, fmt.Errorf("worktree: git add -A: %w (output: %s)", addErr, strings.TrimSpace(string(addOut)))
	}

	commitCmd := exec.CommandContext(ctx, "git", "commit", "-m", message)
	commitCmd.Dir = w.Path
	if commitOut, commitErr := commitCmd.CombinedOutput(); commitErr != nil {
		return false, fmt.Errorf("worktree: git commit: %w (output: %s)", commitErr, strings.TrimSpace(string(commitOut)))
	}

	return true, nil
}

// MergeResult is the outcome of a MergeInto operation.
type MergeResult struct {
	// Merged is true when the merge completed without conflicts.
	Merged bool
	// ConflictFiles lists the files with merge conflicts (non-empty when Merged is false).
	ConflictFiles []string
}

// MergeInto merges the worktree branch into targetBranch (usually the branch the
// user was on when the run started, typically "main" or current HEAD).
// If a merge conflict occurs, it aborts the merge (leaving the repo clean) and
// returns a MergeResult with Merged=false and the conflicting files.
func (w Worktree) MergeInto(ctx context.Context, targetBranch, mergeMsg string) (MergeResult, error) {
	// Switch main repo to target branch
	checkoutCmd := exec.CommandContext(ctx, "git", "checkout", targetBranch)
	checkoutCmd.Dir = w.RepoPath
	if out, err := checkoutCmd.CombinedOutput(); err != nil {
		return MergeResult{}, fmt.Errorf("worktree: checkout %q: %w (output: %s)", targetBranch, err, strings.TrimSpace(string(out)))
	}

	// Attempt the merge
	mergeCmd := exec.CommandContext(ctx, "git", "merge", "--no-ff", "-m", mergeMsg, w.Branch)
	mergeCmd.Dir = w.RepoPath
	mergeOut, mergeErr := mergeCmd.CombinedOutput()
	if mergeErr == nil {
		return MergeResult{Merged: true}, nil
	}

	// Merge failed — check for conflicts
	conflictFiles, listErr := listConflicts(ctx, w.RepoPath)
	if listErr != nil {
		// Abort to keep repo clean, then propagate
		_ = abortMerge(ctx, w.RepoPath) // fire-and-forget: best-effort cleanup before returning error
		return MergeResult{}, fmt.Errorf("worktree: merge %q: %w (output: %s)", w.Branch, mergeErr, strings.TrimSpace(string(mergeOut)))
	}

	if len(conflictFiles) == 0 {
		// Some non-conflict error (e.g. up to date) — abort and return the error
		_ = abortMerge(ctx, w.RepoPath) // fire-and-forget: best-effort cleanup before returning error
		return MergeResult{}, fmt.Errorf("worktree: merge %q: %w (output: %s)", w.Branch, mergeErr, strings.TrimSpace(string(mergeOut)))
	}

	// Conflicts found — abort and surface them to the caller for interactive resolution
	if abortErr := abortMerge(ctx, w.RepoPath); abortErr != nil {
		return MergeResult{}, fmt.Errorf("worktree: abort merge after conflicts: %w", abortErr)
	}

	return MergeResult{Merged: false, ConflictFiles: conflictFiles}, nil
}

// CurrentBranch returns the current branch of the main repo.
func CurrentBranch(ctx context.Context, repoPath string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("worktree: git rev-parse HEAD: %w", err)
	}
	branch := strings.TrimSpace(string(out))
	if branch == "HEAD" {
		return "", fmt.Errorf("worktree: repository is in detached HEAD state")
	}
	return branch, nil
}

// listConflicts returns the files that have merge conflicts in the repo.
func listConflicts(ctx context.Context, repoPath string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--name-only", "--diff-filter=U")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list conflicts: %w", err)
	}
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// abortMerge runs git merge --abort in the repo.
func abortMerge(ctx context.Context, repoPath string) error {
	cmd := exec.CommandContext(ctx, "git", "merge", "--abort")
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git merge --abort: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
