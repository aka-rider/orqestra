package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGitDiffSummary_ModifiedFile(t *testing.T) {
	repoDir := initTempRepo(t)

	// Modify an existing file.
	os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\n\nfunc main() { println(\"hello\") }\n"), 0644)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	diff, err := GitDiffSummary(ctx, repoDir)
	if err != nil {
		t.Fatalf("GitDiffSummary: %v", err)
	}

	if diff.StatSummary == "" {
		t.Error("StatSummary is empty, expected change summary")
	}
	if !strings.Contains(diff.StatSummary, "main.go") {
		t.Errorf("StatSummary = %q, want to contain main.go", diff.StatSummary)
	}

	if len(diff.ChangedFiles) != 1 {
		t.Fatalf("ChangedFiles = %d, want 1", len(diff.ChangedFiles))
	}
	if diff.ChangedFiles[0].Status != "M" {
		t.Errorf("Status = %q, want M", diff.ChangedFiles[0].Status)
	}
	if diff.ChangedFiles[0].Path != "main.go" {
		t.Errorf("Path = %q, want main.go", diff.ChangedFiles[0].Path)
	}
}

func TestGitDiffSummary_NewAndDeletedFiles(t *testing.T) {
	repoDir := initTempRepo(t)

	// Add a new file (unstaged).
	os.WriteFile(filepath.Join(repoDir, "new.txt"), []byte("new content"), 0644)

	// Delete an existing file.
	os.Remove(filepath.Join(repoDir, "main.go"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	diff, err := GitDiffSummary(ctx, repoDir)
	if err != nil {
		t.Fatalf("GitDiffSummary: %v", err)
	}

	// Unstaged diff only sees deleted tracked file (not new untracked).
	found := false
	for _, f := range diff.ChangedFiles {
		if f.Status == "D" && f.Path == "main.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected deleted main.go in ChangedFiles, got %v", diff.ChangedFiles)
	}
}

func TestGitDiffSummaryStaged_IncludesAddedFiles(t *testing.T) {
	repoDir := initTempRepo(t)

	// Add a new file and stage it.
	os.WriteFile(filepath.Join(repoDir, "added.go"), []byte("package added\n"), 0644)
	cmd := exec.Command("git", "add", "added.go")
	cmd.Dir = repoDir
	cmd.Run()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	diff, err := GitDiffSummaryStaged(ctx, repoDir)
	if err != nil {
		t.Fatalf("GitDiffSummaryStaged: %v", err)
	}

	found := false
	for _, f := range diff.ChangedFiles {
		if f.Status == "A" && f.Path == "added.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected added.go in ChangedFiles, got %v", diff.ChangedFiles)
	}
}

func TestGitDiffSummary_NoChanges(t *testing.T) {
	repoDir := initTempRepo(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	diff, err := GitDiffSummary(ctx, repoDir)
	if err != nil {
		t.Fatalf("GitDiffSummary: %v", err)
	}

	if len(diff.ChangedFiles) != 0 {
		t.Errorf("expected no changes, got %v", diff.ChangedFiles)
	}
	if strings.TrimSpace(diff.StatSummary) != "" {
		t.Errorf("StatSummary should be empty, got %q", diff.StatSummary)
	}
}

// initTempRepo creates a temp git repo with a single committed file.
func initTempRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init", "-q"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init step %v: %v\n%s", args, err, out)
		}
	}

	// Create and commit a file.
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644)
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial", "-q")
	cmd.Dir = dir
	cmd.Run()

	return dir
}
