package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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

// Head returns the full SHA of the given ref (branch name, tag, etc.) in the main repo.
func (w Worktree) Head(ctx context.Context, ref string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", ref)
	cmd.Dir = w.RepoPath
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("worktree: rev-parse %q: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// StageAll stages all changes in the worktree. Returns (true, nil) when there
// is something staged; (false, nil) when the working tree is clean.
func (w Worktree) StageAll(ctx context.Context) (bool, error) {
	addCmd := exec.CommandContext(ctx, "git", "add", "-A")
	addCmd.Dir = w.Path
	if out, err := addCmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("worktree: git add -A: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	statusCmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	statusCmd.Dir = w.Path
	out, err := statusCmd.Output()
	if err != nil {
		return false, fmt.Errorf("worktree: git status: %w", err)
	}
	return len(strings.TrimSpace(string(out))) > 0, nil
}

// Diff returns the staged diff against base (a branch name or commit SHA).
// Call StageAll before Diff to capture all working-tree changes.
func (w Worktree) Diff(ctx context.Context, base string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--cached", base)
	cmd.Dir = w.Path
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("worktree: git diff --cached %q: %w", base, err)
	}
	return string(out), nil
}

// CommitStaged creates a commit from whatever is currently staged.
func (w Worktree) CommitStaged(ctx context.Context, message string) error {
	cmd := exec.CommandContext(ctx, "git", "commit", "-m", message)
	cmd.Dir = w.Path
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("worktree: git commit: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// StageFiles stages the given files (relative to the worktree root).
func (w Worktree) StageFiles(ctx context.Context, files []string) error {
	if len(files) == 0 {
		return nil
	}
	args := append([]string{"add", "--"}, files...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = w.Path
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("worktree: git add files: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// MergeBaseIntoWorktree merges base (a branch in the main repo) into the
// worktree's current branch. On conflict the markers are LEFT in place (not
// aborted) and the sorted conflict files are returned. The caller must call
// AbortMergeInWorktree on any give-up path.
func (w Worktree) MergeBaseIntoWorktree(ctx context.Context, base string) (MergeResult, error) {
	cmd := exec.CommandContext(ctx, "git", "merge", base)
	cmd.Dir = w.Path
	mergeOut, mergeErr := cmd.CombinedOutput()
	if mergeErr == nil {
		return MergeResult{Merged: true}, nil
	}

	conflictFiles, listErr := listConflictsIn(ctx, w.Path)
	if listErr != nil {
		_ = w.AbortMergeInWorktree(ctx) // fire-and-forget: best-effort cleanup before returning error
		return MergeResult{}, fmt.Errorf("worktree: merge %q: %w (output: %s)", base, mergeErr, strings.TrimSpace(string(mergeOut)))
	}
	if len(conflictFiles) == 0 {
		_ = w.AbortMergeInWorktree(ctx) // fire-and-forget: best-effort cleanup before returning error
		return MergeResult{}, fmt.Errorf("worktree: merge %q: %w (output: %s)", base, mergeErr, strings.TrimSpace(string(mergeOut)))
	}

	// Leave markers in place for potential resolution.
	sort.Strings(conflictFiles)
	return MergeResult{Merged: false, ConflictFiles: conflictFiles}, nil
}

// AbortMergeInWorktree runs git merge --abort inside the worktree directory.
func (w Worktree) AbortMergeInWorktree(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "git", "merge", "--abort")
	cmd.Dir = w.Path
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("worktree: git merge --abort: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// FastForwardBase checks out base in the main repo and fast-forwards it to the
// worktree's branch tip. Fails if the base cannot be reached as a fast-forward.
func (w Worktree) FastForwardBase(ctx context.Context, base string) error {
	checkoutCmd := exec.CommandContext(ctx, "git", "checkout", base)
	checkoutCmd.Dir = w.RepoPath
	if out, err := checkoutCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("worktree: checkout %q: %w (output: %s)", base, err, strings.TrimSpace(string(out)))
	}
	mergeCmd := exec.CommandContext(ctx, "git", "merge", "--ff-only", w.Branch)
	mergeCmd.Dir = w.RepoPath
	if out, err := mergeCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("worktree: ff-merge %q into %q: %w (output: %s)", w.Branch, base, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// UnresolvedPaths returns the sorted list of files with unmerged index entries.
func (w Worktree) UnresolvedPaths(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--name-only", "--diff-filter=U")
	cmd.Dir = w.Path
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("worktree: unresolved paths: %w", err)
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			paths = append(paths, line)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// MarkerScan returns the sorted subset of paths that still contain conflict
// markers (<<<<<< / ======= / >>>>>>).
func (w Worktree) MarkerScan(paths []string) ([]string, error) {
	var withMarkers []string
	for _, p := range paths {
		data, err := os.ReadFile(filepath.Join(w.Path, p))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("worktree: marker scan %q: %w", p, err)
		}
		if hasConflictMarker(string(data)) {
			withMarkers = append(withMarkers, p)
		}
	}
	sort.Strings(withMarkers)
	return withMarkers, nil
}

// ChangedPaths returns the sorted list of files that have working-tree or
// index modifications, including untracked files. Untracked files are included
// so that files created by the LLM outside the conflict set are detected by
// ResolutionClean's blast-radius check.
func (w Worktree) ChangedPaths(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = w.Path
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("worktree: changed paths: %w", err)
	}
	seen := map[string]bool{}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 4 {
			continue
		}
		p := strings.TrimSpace(line[3:])
		// Handle rename notation "old -> new".
		if i := strings.Index(p, " -> "); i != -1 {
			p = p[i+4:]
		}
		if p != "" && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// ResolutionClean checks that a conflict has been cleanly resolved using
// working-tree state (called before staging):
//   - no conflict markers remain in conflictFiles (MarkerScan; working-tree content)
//   - all changes are confined to conflictFiles (ChangedPaths; blast-radius guard)
//
// UnresolvedPaths is intentionally omitted: the git index always shows unmerged
// entries until git-add, so checking it before staging produces a false failure on
// every clean resolution. MarkerScan covers the same failure mode via content.
//
// Returns (true, "", nil) when the tree is clean. On any failure reason is a
// human-readable explanation. The checks are pure git-structural facts; no
// correctness about the resolution is asserted.
func (w Worktree) ResolutionClean(ctx context.Context, conflictFiles []string) (bool, string, error) {
	withMarkers, err := w.MarkerScan(conflictFiles)
	if err != nil {
		return false, "", fmt.Errorf("worktree: ResolutionClean: marker scan: %w", err)
	}
	if len(withMarkers) > 0 {
		return false, fmt.Sprintf("conflict markers remain in: %s", strings.Join(withMarkers, ", ")), nil
	}

	changed, err := w.ChangedPaths(ctx)
	if err != nil {
		return false, "", fmt.Errorf("worktree: ResolutionClean: changed paths: %w", err)
	}
	conflictSet := make(map[string]bool, len(conflictFiles))
	for _, f := range conflictFiles {
		conflictSet[f] = true
	}
	var outOfScope []string
	for _, p := range changed {
		if !conflictSet[p] {
			outOfScope = append(outOfScope, p)
		}
	}
	if len(outOfScope) > 0 {
		return false, fmt.Sprintf("changes outside conflict set: %s", strings.Join(outOfScope, ", ")), nil
	}

	return true, "", nil
}

// listConflictsIn returns files with unmerged index entries in the given dir.
func listConflictsIn(ctx context.Context, dir string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--name-only", "--diff-filter=U")
	cmd.Dir = dir
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

// hasConflictMarker reports whether content contains a conflict marker line.
func hasConflictMarker(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "<<<<<<<") ||
			strings.HasPrefix(line, ">>>>>>>") ||
			strings.HasPrefix(line, "=======") {
			return true
		}
	}
	return false
}
