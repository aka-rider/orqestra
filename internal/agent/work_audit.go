//go:build darwin

package agent

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// WorkDiff captures the git diff summary after worker execution.
type WorkDiff struct {
	// StatSummary is the output of `git diff --stat` (human-readable file change summary).
	StatSummary string
	// NameStatus is the output of `git diff --name-status` (machine-friendly change list).
	NameStatus string
	// ChangedFiles is the parsed list of changed files with their status.
	ChangedFiles []ChangedFile
}

// ChangedFile represents a single file change detected by git diff.
type ChangedFile struct {
	Status string // M=modified, A=added, D=deleted, R=renamed
	Path   string
}

// GitDiffSummary runs git diff against the working tree in the given repo directory
// and returns a structured summary. This is the human audit trail showing what
// the worker actually changed.
func GitDiffSummary(ctx context.Context, repoPath string) (*WorkDiff, error) {
	stat, err := gitCmd(ctx, repoPath, "diff", "--stat")
	if err != nil {
		return nil, fmt.Errorf("git diff --stat: %w", err)
	}

	nameStatus, err := gitCmd(ctx, repoPath, "diff", "--name-status")
	if err != nil {
		return nil, fmt.Errorf("git diff --name-status: %w", err)
	}

	files := parseNameStatus(nameStatus)

	return &WorkDiff{
		StatSummary:  stat,
		NameStatus:   nameStatus,
		ChangedFiles: files,
	}, nil
}

// GitDiffSummaryStaged is like GitDiffSummary but includes staged (added) files.
// Useful when the worker has run `git add` on new files.
func GitDiffSummaryStaged(ctx context.Context, repoPath string) (*WorkDiff, error) {
	// Unstaged changes.
	unstaged, err := GitDiffSummary(ctx, repoPath)
	if err != nil {
		return nil, err
	}

	// Staged changes.
	stagedStat, err := gitCmd(ctx, repoPath, "diff", "--cached", "--stat")
	if err != nil {
		return nil, fmt.Errorf("git diff --cached --stat: %w", err)
	}
	stagedNS, err := gitCmd(ctx, repoPath, "diff", "--cached", "--name-status")
	if err != nil {
		return nil, fmt.Errorf("git diff --cached --name-status: %w", err)
	}

	// Merge: combine unstaged and staged.
	combined := &WorkDiff{
		StatSummary:  strings.TrimSpace(unstaged.StatSummary + "\n" + stagedStat),
		NameStatus:   strings.TrimSpace(unstaged.NameStatus + "\n" + stagedNS),
		ChangedFiles: append(unstaged.ChangedFiles, parseNameStatus(stagedNS)...),
	}
	return combined, nil
}

func gitCmd(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s (stderr: %s)", err, stderr.String())
	}
	return stdout.String(), nil
}

func parseNameStatus(output string) []ChangedFile {
	var files []ChangedFile
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		files = append(files, ChangedFile{
			Status: parts[0],
			Path:   parts[1],
		})
	}
	return files
}
