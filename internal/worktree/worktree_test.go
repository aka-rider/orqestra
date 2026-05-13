//go:build integration

package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	dir := t.TempDir()
	ctx := context.Background()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (output: %s)", args, err, out)
		}
	}

	run("init", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("init\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "init")

	return dir
}

// Contract: worktree.go — Create adds a branch, Remove cleans it up
func TestWorktree_CreateAndRemove(t *testing.T) {
	ctx := context.Background()
	repoPath := initRepo(t)
	sessionDir := filepath.Join(t.TempDir(), "session")

	wt, err := Create(ctx, repoPath, sessionDir, "run-1")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if wt.Branch != "orqestra-run-run-1" {
		t.Errorf("Branch = %q, want orqestra-run-run-1", wt.Branch)
	}
	if wt.Path == "" {
		t.Error("Path must be non-empty")
	}

	if err := wt.Remove(ctx, false); err != nil {
		t.Fatalf("Remove() error: %v", err)
	}
}

// Contract: worktree.go — CommitAll stages and commits all changes
func TestWorktree_CommitAll(t *testing.T) {
	ctx := context.Background()
	repoPath := initRepo(t)
	sessionDir := filepath.Join(t.TempDir(), "session")

	wt, err := Create(ctx, repoPath, sessionDir, "run-commit")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	t.Cleanup(func() { wt.Remove(ctx, true) })

	if err := os.WriteFile(filepath.Join(wt.Path, "work.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	committed, err := wt.CommitAll(ctx, "add work.txt")
	if err != nil {
		t.Fatalf("CommitAll() error: %v", err)
	}
	if !committed {
		t.Error("CommitAll() = false, want true (changes exist)")
	}

	// Second call on clean tree should return false
	committed2, err := wt.CommitAll(ctx, "nothing to commit")
	if err != nil {
		t.Fatalf("CommitAll() second call error: %v", err)
	}
	if committed2 {
		t.Error("CommitAll() = true on clean tree, want false")
	}
}

// Contract: worktree.go — MergeInto merges branch back; MergeResult.Merged = true on clean merge
func TestWorktree_MergeInto_Clean(t *testing.T) {
	ctx := context.Background()
	repoPath := initRepo(t)
	sessionDir := filepath.Join(t.TempDir(), "session")

	wt, err := Create(ctx, repoPath, sessionDir, "run-merge")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	t.Cleanup(func() { wt.Remove(ctx, true) })

	if err := os.WriteFile(filepath.Join(wt.Path, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.CommitAll(ctx, "add feature.txt"); err != nil {
		t.Fatalf("CommitAll() error: %v", err)
	}

	result, err := wt.MergeInto(ctx, "main")
	if err != nil {
		t.Fatalf("MergeInto() error: %v", err)
	}
	if !result.Merged {
		t.Error("MergeResult.Merged = false, want true")
	}
	if len(result.ConflictFiles) != 0 {
		t.Errorf("ConflictFiles = %v, want empty", result.ConflictFiles)
	}
}

// Contract: worktree.go — MergeInto populates ConflictFiles when merge conflicts occur
func TestWorktree_MergeInto_Conflict(t *testing.T) {
	ctx := context.Background()
	repoPath := initRepo(t)
	sessionDir := filepath.Join(t.TempDir(), "session")

	wt, err := Create(ctx, repoPath, sessionDir, "run-conflict")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	t.Cleanup(func() { wt.Remove(ctx, true) })

	// Conflicting commit on the worktree branch
	if err := os.WriteFile(filepath.Join(wt.Path, "README.md"), []byte("branch version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.CommitAll(ctx, "branch change"); err != nil {
		t.Fatalf("CommitAll() error: %v", err)
	}

	// Conflicting commit directly on main (repoPath is checked out to main after initRepo)
	if err := os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("main version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	addCmd := exec.CommandContext(ctx, "git", "-C", repoPath, "add", "-A")
	if out, err := addCmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v (output: %s)", err, out)
	}
	commitCmd := exec.CommandContext(ctx, "git", "-C", repoPath, "commit", "-m", "main change")
	if out, err := commitCmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v (output: %s)", err, out)
	}

	result, err := wt.MergeInto(ctx, "main")
	if err != nil {
		t.Fatalf("MergeInto() error: %v", err)
	}
	if result.Merged {
		t.Error("MergeResult.Merged = true, want false (conflict expected)")
	}
	if len(result.ConflictFiles) == 0 {
		t.Error("expected non-empty ConflictFiles")
	}
}
