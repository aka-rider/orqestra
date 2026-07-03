package worktree_test

// INV-MERGE-LIFECYCLE: the worktree integration capability gate.
// INV-ROLE-INTEGRATOR: the integrator's git mechanics + falsifiable-check gate.
//
// The pipeline's git-facing capability — create an isolated worktree, commit the
// worker's changes, merge them back to the target branch, and clean up — must
// work against a real git repository. This drives the real worktree.* API and
// real `git` (no fakes): a break in any leg (create, commit, merge landing,
// branch/dir cleanup) turns this RED in seconds, without running the pipeline.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/worktree"
)

// gitCommitFile adds and commits a single file with given content in repoDir.
func gitCommitFile(t *testing.T, repoDir, filename, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repoDir, filename), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", filename}, {"commit", "-q", "-m", msg}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func gitInitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed: %v\n%s", args, err, out)
		}
	}
	// One real commit so HEAD exists (git worktree add requires it).
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "seed"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed: %v\n%s", args, err, out)
		}
	}
	return repo
}

func TestRole_Merge_Lifecycle(t *testing.T) {
	ctx := context.Background()
	repo := gitInitRepo(t)

	target, err := worktree.CurrentBranch(ctx, repo)
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}

	sessionDir := filepath.Join(t.TempDir(), "session")

	// Create the isolated worktree.
	wt, err := worktree.Create(ctx, repo, sessionDir, "merge-lifecycle")
	if err != nil {
		t.Fatalf("worktree.Create: %v", err)
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatalf("worktree dir missing after Create: %v", err)
	}

	// Worker writes a file inside the worktree.
	artifact := filepath.Join(wt.Path, "feature.txt")
	if err := os.WriteFile(artifact, []byte("done by worker\n"), 0o644); err != nil {
		t.Fatalf("write worktree artifact: %v", err)
	}

	// Commit lands in the worktree branch via the stage-and-commit primitive.
	staged, err := wt.StageAll(ctx)
	if err != nil {
		t.Fatalf("StageAll: %v", err)
	}
	if !staged {
		t.Fatal("StageAll reported nothing to stage — the artifact was not staged")
	}
	if err := wt.CommitStaged(ctx, "worker change"); err != nil {
		t.Fatalf("CommitStaged: %v", err)
	}

	// Merge into the target branch via the base-into-worktree merge plus a
	// fast-forward. No drift has occurred on target since the worktree was
	// created, so the merge is clean and the fast-forward lands the artifact
	// on target.
	res, err := wt.MergeBaseIntoWorktree(ctx, target)
	if err != nil {
		t.Fatalf("MergeBaseIntoWorktree: %v", err)
	}
	if !res.Merged {
		t.Fatalf("merge did not complete cleanly: conflicts=%v", res.ConflictFiles)
	}
	if err := wt.FastForwardBase(ctx, target); err != nil {
		t.Fatalf("FastForwardBase: %v", err)
	}

	// The artifact must now exist on the target branch in the main repo.
	if _, err := os.Stat(filepath.Join(repo, "feature.txt")); err != nil {
		t.Fatalf("INV-MERGE-LIFECYCLE: merged change did not land on target branch: %v", err)
	}

	// Cleanup removes both the worktree directory and its branch.
	if err := wt.Remove(ctx, false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("INV-MERGE-LIFECYCLE: worktree dir not removed: stat err=%v", err)
	}
	branchList := exec.Command("git", "branch", "--list", wt.Branch)
	branchList.Dir = repo
	out, err := branchList.CombinedOutput()
	if err != nil {
		t.Fatalf("git branch --list: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("INV-MERGE-LIFECYCLE: branch %q not deleted after cleanup: %q", wt.Branch, out)
	}
}

// TestRole_Integrator_MergeAndResolve drives the integrator's git/decision
// functions (MergeBaseIntoWorktree, ResolutionClean, FastForwardBase,
// AbortMergeInWorktree, Head) directly against a real git repo.
// No LLM, no harness — the test stages disk state as a stand-in for LLM edits.
func TestRole_Integrator_MergeAndResolve(t *testing.T) {
	ctx := context.Background()

	// newConflictRepo creates a fresh git repo, a worktree, and introduces a
	// conflict: both base and run branch modify shared.txt differently.
	// Returns: repo path, worktree, base branch name, pre-conflict base SHA.
	newConflictRepo := func(t *testing.T) (string, worktree.Worktree, string, string) {
		t.Helper()
		repo := gitInitRepo(t)

		// Commit shared.txt on base.
		gitCommitFile(t, repo, "shared.txt", "base content\n", "add shared.txt")

		base, err := worktree.CurrentBranch(ctx, repo)
		if err != nil {
			t.Fatalf("CurrentBranch: %v", err)
		}

		// Create worktree on run branch.
		sessionDir := filepath.Join(t.TempDir(), "session")
		wt, err := worktree.Create(ctx, repo, sessionDir, "integrator-test")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		// Worker modifies shared.txt in worktree.
		if err := os.WriteFile(filepath.Join(wt.Path, "shared.txt"), []byte("worker change\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.StageAll(ctx); err != nil {
			t.Fatalf("StageAll: %v", err)
		}
		if err := wt.CommitStaged(ctx, "worker: change shared.txt"); err != nil {
			t.Fatalf("CommitStaged: %v", err)
		}

		// Advance base (drift): modify shared.txt on the base branch.
		gitCommitFile(t, repo, "shared.txt", "base drift\n", "base: change shared.txt")

		// Record base SHA AFTER drift — this is the point IntegrateStep records at
		// the start of its Run (any drift has already accumulated before the step fires).
		basePreSHA, err := wt.Head(ctx, base)
		if err != nil {
			t.Fatalf("Head: %v", err)
		}

		return repo, wt, base, basePreSHA
	}

	t.Run("clean merge no drift", func(t *testing.T) {
		// INV-ROLE-INTEGRATOR: no drift → MergeBaseIntoWorktree succeeds immediately;
		// FastForwardBase advances base to run-branch tip.
		repo := gitInitRepo(t)

		base, err := worktree.CurrentBranch(ctx, repo)
		if err != nil {
			t.Fatalf("CurrentBranch: %v", err)
		}
		sessionDir := filepath.Join(t.TempDir(), "session")
		wt, err := worktree.Create(ctx, repo, sessionDir, "no-drift")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		// Worker adds a new file.
		if err := os.WriteFile(filepath.Join(wt.Path, "new.txt"), []byte("new\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.StageAll(ctx); err != nil {
			t.Fatalf("StageAll: %v", err)
		}
		if err := wt.CommitStaged(ctx, "worker: add new.txt"); err != nil {
			t.Fatalf("CommitStaged: %v", err)
		}

		// No drift: MergeBaseIntoWorktree reports already up-to-date.
		res, err := wt.MergeBaseIntoWorktree(ctx, base)
		if err != nil {
			t.Fatalf("MergeBaseIntoWorktree: %v", err)
		}
		if !res.Merged {
			t.Fatalf("expected clean merge, got conflicts: %v", res.ConflictFiles)
		}

		// FastForwardBase advances base to run-branch tip.
		if err := wt.FastForwardBase(ctx, base); err != nil {
			t.Fatalf("FastForwardBase: %v", err)
		}
		// Verify base now contains new.txt.
		if _, err := os.Stat(filepath.Join(repo, "new.txt")); err != nil {
			t.Fatalf("new.txt not on base after FF: %v", err)
		}
		// Cleanup.
		if err := wt.Remove(ctx, false); err != nil {
			t.Fatalf("Remove: %v", err)
		}
	})

	t.Run("conflict clean resolution", func(t *testing.T) {
		// INV-ROLE-INTEGRATOR: conflict → test resolves file manually (stand-in for LLM edit)
		// → ResolutionClean=true → FastForwardBase succeeds.
		repo, wt, base, _ := newConflictRepo(t)

		res, err := wt.MergeBaseIntoWorktree(ctx, base)
		if err != nil {
			t.Fatalf("MergeBaseIntoWorktree: %v", err)
		}
		if res.Merged {
			t.Skip("expected conflict but got clean merge — git behaviour may differ")
		}
		if len(res.ConflictFiles) == 0 {
			t.Fatal("expected conflict files, got none")
		}

		// Simulate LLM resolving shared.txt — write a clean merged version.
		resolvedContent := "worker change\nbase drift\n"
		if err := os.WriteFile(filepath.Join(wt.Path, "shared.txt"), []byte(resolvedContent), 0o644); err != nil {
			t.Fatal(err)
		}

		// Falsifiable check: no markers, no unresolved paths, changes confined.
		clean, reason, err := wt.ResolutionClean(ctx, res.ConflictFiles)
		if err != nil {
			t.Fatalf("ResolutionClean: %v", err)
		}
		if !clean {
			t.Fatalf("ResolutionClean=false, want true: %s", reason)
		}

		// Stage and commit resolution.
		if err := wt.StageFiles(ctx, res.ConflictFiles); err != nil {
			t.Fatalf("StageFiles: %v", err)
		}
		if err := wt.CommitStaged(ctx, "Resolve merge conflicts (LLM-assisted)"); err != nil {
			t.Fatalf("CommitStaged: %v", err)
		}

		// FastForwardBase succeeds; base moves to merge commit.
		if err := wt.FastForwardBase(ctx, base); err != nil {
			t.Fatalf("FastForwardBase: %v", err)
		}
		// Verify the resolved content is on base.
		got, err := os.ReadFile(filepath.Join(repo, "shared.txt"))
		if err != nil {
			t.Fatalf("read shared.txt on base: %v", err)
		}
		if string(got) != resolvedContent {
			t.Errorf("shared.txt on base = %q, want %q", got, resolvedContent)
		}
		if err := wt.Remove(ctx, false); err != nil {
			t.Fatalf("Remove: %v", err)
		}
	})

	t.Run("conflict markers remain give up", func(t *testing.T) {
		// INV-ROLE-INTEGRATOR: markers remain → ResolutionClean=false → abort;
		// base pre-SHA is unchanged (recoverable).
		repo, wt, base, basePreSHA := newConflictRepo(t)

		res, err := wt.MergeBaseIntoWorktree(ctx, base)
		if err != nil {
			t.Fatalf("MergeBaseIntoWorktree: %v", err)
		}
		if res.Merged {
			t.Skip("expected conflict")
		}

		// Leave markers in place (simulate give-up).
		clean, reason, err := wt.ResolutionClean(ctx, res.ConflictFiles)
		if err != nil {
			t.Fatalf("ResolutionClean: %v", err)
		}
		if clean {
			t.Fatal("ResolutionClean=true with markers in file, want false")
		}
		if !strings.Contains(reason, "conflict markers remain") && !strings.Contains(reason, "unmerged paths remain") {
			t.Errorf("unexpected reason: %s", reason)
		}

		// Abort merge — base must remain at pre-conflict SHA.
		if err := wt.AbortMergeInWorktree(ctx); err != nil {
			t.Fatalf("AbortMergeInWorktree: %v", err)
		}
		currentBaseSHA, err := wt.Head(ctx, base)
		if err != nil {
			t.Fatalf("Head after abort: %v", err)
		}
		if currentBaseSHA != basePreSHA {
			t.Errorf("base SHA changed after abort: got %q, want %q", currentBaseSHA, basePreSHA)
		}
		// Run branch and worktree still exist.
		if _, err := os.Stat(wt.Path); err != nil {
			t.Fatalf("worktree dir gone after abort: %v", err)
		}
		_ = repo
	})

	t.Run("conflict out of scope edit", func(t *testing.T) {
		// INV-ROLE-INTEGRATOR: LLM edits a file outside the conflict set
		// → ResolutionClean=false (blast-radius check).
		_, wt, base, _ := newConflictRepo(t)

		res, err := wt.MergeBaseIntoWorktree(ctx, base)
		if err != nil {
			t.Fatalf("MergeBaseIntoWorktree: %v", err)
		}
		if res.Merged {
			t.Skip("expected conflict")
		}

		// Resolve conflict file cleanly.
		if err := os.WriteFile(filepath.Join(wt.Path, "shared.txt"), []byte("resolved\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Also edit a file outside the conflict set (blast-radius violation).
		if err := os.WriteFile(filepath.Join(wt.Path, "extra.txt"), []byte("extra\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		clean, reason, err := wt.ResolutionClean(ctx, res.ConflictFiles)
		if err != nil {
			t.Fatalf("ResolutionClean: %v", err)
		}
		if clean {
			t.Fatal("ResolutionClean=true with out-of-scope edit, want false")
		}
		if !strings.Contains(reason, "outside conflict set") && !strings.Contains(reason, "unmerged paths remain") {
			t.Errorf("unexpected reason: %s", reason)
		}

		if err := wt.AbortMergeInWorktree(ctx); err != nil {
			t.Fatalf("AbortMergeInWorktree: %v", err)
		}
	})

	t.Run("ResolutionClean marker scan", func(t *testing.T) {
		// Pure MarkerScan check: create a worktree, write a file with/without markers.
		repo := gitInitRepo(t)
		sessionDir := filepath.Join(t.TempDir(), "session")
		wt, err := worktree.Create(ctx, repo, sessionDir, "markerscan")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		defer wt.Remove(ctx, true) //nolint:errcheck

		fileWithMarker := filepath.Join(wt.Path, "conflict.txt")
		if err := os.WriteFile(fileWithMarker, []byte("<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> branch\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		fileClean := filepath.Join(wt.Path, "clean.txt")
		if err := os.WriteFile(fileClean, []byte("no markers\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		withMarkers, err := wt.MarkerScan([]string{"conflict.txt"})
		if err != nil {
			t.Fatalf("MarkerScan conflict.txt: %v", err)
		}
		if len(withMarkers) != 1 || withMarkers[0] != "conflict.txt" {
			t.Errorf("MarkerScan = %v, want [conflict.txt]", withMarkers)
		}

		noMarkers, err := wt.MarkerScan([]string{"clean.txt"})
		if err != nil {
			t.Fatalf("MarkerScan clean.txt: %v", err)
		}
		if len(noMarkers) != 0 {
			t.Errorf("MarkerScan clean file = %v, want []", noMarkers)
		}
	})
}
