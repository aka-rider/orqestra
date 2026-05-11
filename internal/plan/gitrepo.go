package plan

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// GitRepo is a single-file git repository for tracking plan versions.
// It lives inside the session directory and is never pushed or branched.
type GitRepo struct {
	dir string // absolute path to plan-history/ subdirectory
}

// NewGitRepo initializes a git repo at {sessionPath}/plan-history/.
// Returns an error if git is not available or init fails.
func NewGitRepo(sessionPath string) (*GitRepo, error) {
	dir := filepath.Join(sessionPath, "plan-history")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create plan-history dir: %w", err)
	}
	if err := gitRun(dir, "init", "--quiet"); err != nil {
		return nil, fmt.Errorf("git init: %w", err)
	}
	if err := gitRun(dir, "config", "user.name", "orqestra"); err != nil {
		return nil, fmt.Errorf("git config user.name: %w", err)
	}
	if err := gitRun(dir, "config", "user.email", "plan@orqestra.local"); err != nil {
		return nil, fmt.Errorf("git config user.email: %w", err)
	}
	return &GitRepo{dir: dir}, nil
}

// Commit writes the plan markdown to plan.md, stages, and commits.
func (r *GitRepo) Commit(markdown, message string) error {
	planPath := filepath.Join(r.dir, "plan.md")
	if err := os.WriteFile(planPath, []byte(markdown), 0o644); err != nil {
		return fmt.Errorf("write plan.md: %w", err)
	}
	if err := gitRun(r.dir, "add", "plan.md"); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	if err := gitRun(r.dir, "commit", "--quiet", "-m", message, "--allow-empty"); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}

// Diff returns the unified diff between the previous and current plan version.
// Returns "" if there is only one commit (no previous version).
func (r *GitRepo) Diff() (string, error) {
	if !r.HasHistory() {
		return "", nil
	}
	out, err := exec.Command("git", "-C", r.dir, "diff", "--color=always", "HEAD~1", "HEAD", "--", "plan.md").Output()
	if err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	return string(out), nil
}

// HasHistory returns true if there are at least 2 commits (a diff is available).
func (r *GitRepo) HasHistory() bool {
	out, err := exec.Command("git", "-C", r.dir, "rev-list", "--count", "HEAD").Output()
	if err != nil {
		return false
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return false
	}
	return count > 1
}

// Log returns the oneline commit log for plan.md.
func (r *GitRepo) Log() (string, error) {
	out, err := exec.Command("git", "-C", r.dir, "log", "--oneline", "--", "plan.md").Output()
	if err != nil {
		return "", fmt.Errorf("git log: %w", err)
	}
	return string(out), nil
}

// PlanPath returns the absolute path to the tracked plan.md file.
func (r *GitRepo) PlanPath() string {
	return filepath.Join(r.dir, "plan.md")
}

func gitRun(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}
