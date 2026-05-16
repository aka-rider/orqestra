package plan

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Revision is a single committed plan revision read from the plan-history
// micro-repo. It is read-only metadata; full content is fetched via
// (*GitRepo).ContentAt.
type Revision struct {
	SHA      string
	ShortSHA string
	Time     time.Time
	Author   string
	Subject  string
}

// OpenGitRepo opens an existing plan-history git repo at dir for read-only
// inspection. It does not initialize git state; use NewGitRepo for that. The
// returned *GitRepo is never (nil, nil): any failure returns a wrapped error.
func OpenGitRepo(dir string) (*GitRepo, error) {
	if dir == "" {
		return nil, errors.New("open plan repo: empty dir")
	}
	info, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil {
		return nil, fmt.Errorf("open plan repo %q: stat .git: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("open plan repo %q: .git is not a directory", dir)
	}
	return &GitRepo{dir: dir}, nil
}

// Revisions returns every plan.md revision recorded in the repo, newest first.
// The result is sorted by commit time descending as defense-in-depth on top of
// git's default --date-order.
func (r *GitRepo) Revisions() ([]Revision, error) {
	out, err := exec.Command(
		"git", "-C", r.dir,
		"log",
		"--pretty=format:%H%x09%h%x09%aI%x09%an%x09%s",
		"--",
		"plan.md",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("git log plan.md in %q: %w", r.dir, err)
	}
	text := strings.TrimRight(string(out), "\n")
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	revs := make([]Revision, 0, len(lines))
	for i, line := range lines {
		rev, perr := parseRevLine(line)
		if perr != nil {
			return nil, fmt.Errorf("parse rev line %d in %q: %w", i, r.dir, perr)
		}
		revs = append(revs, rev)
	}
	sort.SliceStable(revs, func(i, j int) bool {
		return revs[i].Time.After(revs[j].Time)
	})
	return revs, nil
}

// ContentAt returns the contents of plan.md at the named commit.
func (r *GitRepo) ContentAt(sha string) (string, error) {
	if sha == "" {
		return "", errors.New("content at: empty sha")
	}
	out, err := exec.Command("git", "-C", r.dir, "show", sha+":plan.md").Output()
	if err != nil {
		return "", fmt.Errorf("git show %s:plan.md in %q: %w", sha, r.dir, err)
	}
	return string(out), nil
}

// DiffBetween returns a plain unified diff of plan.md between base and target.
// `git diff` exits with status 1 when diffs exist; that is success here, only
// other exit codes propagate.
func (r *GitRepo) DiffBetween(base, target string) (string, error) {
	if base == "" || target == "" {
		return "", fmt.Errorf("diff between: empty sha (base=%q target=%q)", base, target)
	}
	cmd := exec.Command("git", "-C", r.dir, "diff", "--no-color", base, target, "--", "plan.md")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return string(out), nil
		}
		return "", fmt.Errorf("git diff %s..%s in %q: %w", base, target, r.dir, err)
	}
	return string(out), nil
}

func parseRevLine(line string) (Revision, error) {
	parts := strings.SplitN(line, "\t", 5)
	if len(parts) != 5 {
		return Revision{}, fmt.Errorf("expected 5 tab-separated fields, got %d", len(parts))
	}
	sha, shortSHA, isoTime, author, subject := parts[0], parts[1], parts[2], parts[3], parts[4]
	if sha == "" {
		return Revision{}, errors.New("empty sha field")
	}
	if shortSHA == "" {
		return Revision{}, errors.New("empty short sha field")
	}
	t, err := time.Parse(time.RFC3339, isoTime)
	if err != nil {
		return Revision{}, fmt.Errorf("parse time field %q: %w", isoTime, err)
	}
	return Revision{
		SHA:      sha,
		ShortSHA: shortSHA,
		Time:     t,
		Author:   author,
		Subject:  subject,
	}, nil
}
