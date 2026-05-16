package plan

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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
// Deprecated: use CommitPlan, CommitDialog, or CommitPlanAndDialog.
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

// Head returns the content of plan.md in the working tree.
// The working tree is always clean after Commit, so this reflects HEAD.
// Returns an error if no commits have been made yet.
func (r *GitRepo) Head() (string, error) {
	data, err := os.ReadFile(filepath.Join(r.dir, "plan.md"))
	if err != nil {
		return "", fmt.Errorf("read plan.md at HEAD: %w", err)
	}
	return string(data), nil
}

// PlanPath returns the absolute path to the tracked plan.md file.
func (r *GitRepo) PlanPath() string {
	return filepath.Join(r.dir, "plan.md")
}

// Dir returns the absolute path to the plan-history directory.
func (r *GitRepo) Dir() string {
	return r.dir
}

func gitRun(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stdout = nil
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

// DialogEntry is one turn in the architect dialog log.
type DialogEntry struct {
	Timestamp    time.Time
	Role         string // "architect", "user", "critic"
	Message      string
	OutputTokens int
}

func formatDialogEntry(e DialogEntry) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("## [%s] %s\n\n", e.Timestamp.Format("2006-01-02 15:04:05"), e.Role))
	sb.WriteString(e.Message)
	if e.OutputTokens > 0 {
		sb.WriteString(fmt.Sprintf(" (%d output tokens)", e.OutputTokens))
	}
	sb.WriteString("\n\n")
	return sb.String()
}

// CommitPlan writes plan.md, appends a "(see plan.md diff)" entry to dialog.md,
// stages both files, and commits.
func (r *GitRepo) CommitPlan(markdown, message string) error {
	planPath := filepath.Join(r.dir, "plan.md")
	if err := os.WriteFile(planPath, []byte(markdown), 0o644); err != nil {
		return fmt.Errorf("write plan.md: %w", err)
	}
	entry := DialogEntry{
		Timestamp: time.Now(),
		Role:      "user",
		Message:   "(see plan.md diff)",
	}
	if err := r.appendDialog(entry); err != nil {
		return err
	}
	if err := gitRun(r.dir, "add", "plan.md", "dialog.md"); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	if err := gitRun(r.dir, "commit", "--quiet", "-m", message, "--allow-empty"); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}

// CommitDialog appends a dialog entry to dialog.md, stages, and commits.
func (r *GitRepo) CommitDialog(entry DialogEntry) error {
	if err := r.appendDialog(entry); err != nil {
		return err
	}
	if err := gitRun(r.dir, "add", "dialog.md"); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	msg := entry.Role + ": " + truncateMsg(entry.Message, 50)
	if err := gitRun(r.dir, "commit", "--quiet", "-m", msg, "--allow-empty"); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}

// CommitPlanAndDialog writes plan.md, appends entry to dialog.md, stages both, and commits.
func (r *GitRepo) CommitPlanAndDialog(markdown string, entry DialogEntry) error {
	planPath := filepath.Join(r.dir, "plan.md")
	if err := os.WriteFile(planPath, []byte(markdown), 0o644); err != nil {
		return fmt.Errorf("write plan.md: %w", err)
	}
	if err := r.appendDialog(entry); err != nil {
		return err
	}
	if err := gitRun(r.dir, "add", "plan.md", "dialog.md"); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	msg := entry.Role + ": " + truncateMsg(entry.Message, 50)
	if err := gitRun(r.dir, "commit", "--quiet", "-m", msg, "--allow-empty"); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}

// DiffPlain returns a plain unified diff of plan.md between sinceHash and HEAD.
// Returns "" if there is no diff or an error occurs.
func (r *GitRepo) DiffPlain(sinceHash string) (string, error) {
	out, err := exec.Command("git", "-C", r.dir, "diff", "--no-color", sinceHash, "HEAD", "--", "plan.md").Output()
	if err != nil {
		return "", nil
	}
	return string(out), nil
}

// HeadCommitHash returns the SHA of the current HEAD commit.
func (r *GitRepo) HeadCommitHash() (string, error) {
	out, err := exec.Command("git", "-C", r.dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (r *GitRepo) appendDialog(entry DialogEntry) error {
	dialogPath := filepath.Join(r.dir, "dialog.md")
	f, err := os.OpenFile(dialogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open dialog.md: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(formatDialogEntry(entry)); err != nil {
		return fmt.Errorf("write dialog.md: %w", err)
	}
	return nil
}

func truncateMsg(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n]
	}
	return s
}
