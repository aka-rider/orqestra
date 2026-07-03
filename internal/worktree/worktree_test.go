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

// Contract: worktree.go — StageAll + CommitStaged stage and commit changes
// (the surviving stage-and-commit primitive). Full merge round-trip coverage
// lives in role_merge_test.go's TestRole_Merge_Lifecycle and
// TestRole_Integrator_MergeAndResolve.
func TestWorktree_StageAllCommitStaged(t *testing.T) {
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

	staged, err := wt.StageAll(ctx)
	if err != nil {
		t.Fatalf("StageAll() error: %v", err)
	}
	if !staged {
		t.Error("StageAll() = false, want true (changes exist)")
	}
	if err := wt.CommitStaged(ctx, "add work.txt"); err != nil {
		t.Fatalf("CommitStaged() error: %v", err)
	}

	// Second call on clean tree should report nothing staged.
	staged2, err := wt.StageAll(ctx)
	if err != nil {
		t.Fatalf("StageAll() second call error: %v", err)
	}
	if staged2 {
		t.Error("StageAll() = true on clean tree, want false")
	}
}
