package plan

import (
	"strings"
	"testing"
)

func TestGitRepo_CommitAndDiff(t *testing.T) {
	repo, err := NewGitRepo(t.TempDir())
	if err != nil {
		t.Fatalf("NewGitRepo: %v", err)
	}

	err = repo.Commit("# Plan\n\n## Goal\nOriginal.\n", "initial plan")
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}

	if repo.HasHistory() {
		t.Error("HasHistory should be false after 1 commit")
	}
	diff, _ := repo.Diff()
	if diff != "" {
		t.Error("Diff should be empty after 1 commit")
	}

	err = repo.Commit("# Plan\n\n## Goal\nRevised.\n", "revision")
	if err != nil {
		t.Fatalf("second commit: %v", err)
	}

	if !repo.HasHistory() {
		t.Error("HasHistory should be true after 2 commits")
	}
	diff, err = repo.Diff()
	if err != nil {
		t.Fatalf("Diff error: %v", err)
	}
	if !strings.Contains(diff, "Original.") || !strings.Contains(diff, "Revised.") {
		t.Errorf("unexpected diff:\n%s", diff)
	}
}

func TestGitRepo_ThreeCommits_DiffShowsLatest(t *testing.T) {
	repo, err := NewGitRepo(t.TempDir())
	if err != nil {
		t.Fatalf("NewGitRepo: %v", err)
	}
	if err := repo.Commit("v1", "first"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Commit("v2", "second"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Commit("v3", "third"); err != nil {
		t.Fatal(err)
	}
	diff, err := repo.Diff()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "v2") || !strings.Contains(diff, "v3") {
		t.Errorf("diff should show v2→v3, got:\n%s", diff)
	}
}

func TestGitRepo_IdenticalCommit(t *testing.T) {
	repo, err := NewGitRepo(t.TempDir())
	if err != nil {
		t.Fatalf("NewGitRepo: %v", err)
	}
	if err := repo.Commit("same", "first"); err != nil {
		t.Fatal(err)
	}
	err = repo.Commit("same", "second") // --allow-empty
	if err != nil {
		t.Fatalf("identical commit should not error: %v", err)
	}
}

func TestGitRepo_Log(t *testing.T) {
	repo, err := NewGitRepo(t.TempDir())
	if err != nil {
		t.Fatalf("NewGitRepo: %v", err)
	}
	if err := repo.Commit("v1", "initial plan"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Commit("v2", "revision: remove WP1"); err != nil {
		t.Fatal(err)
	}
	log, err := repo.Log()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log, "initial plan") || !strings.Contains(log, "revision: remove WP1") {
		t.Errorf("log missing entries:\n%s", log)
	}
}

func TestGitRepo_Head(t *testing.T) {
	repo, err := NewGitRepo(t.TempDir())
	if err != nil {
		t.Fatalf("NewGitRepo: %v", err)
	}

	// Head before any commit should fail.
	_, err = repo.Head()
	if err == nil {
		t.Error("Head should fail before any commits")
	}

	err = repo.Commit("# Plan\n\n## Goal\nFirst version.\n", "architect: initial plan")
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}
	content, err := repo.Head()
	if err != nil {
		t.Fatalf("Head after first commit: %v", err)
	}
	if !strings.Contains(content, "First version.") {
		t.Errorf("Head content = %q, want to contain 'First version.'", content)
	}

	err = repo.Commit("# Plan\n\n## Goal\nSecond version.\n", "user: manual edit")
	if err != nil {
		t.Fatalf("second commit: %v", err)
	}
	content, err = repo.Head()
	if err != nil {
		t.Fatalf("Head after second commit: %v", err)
	}
	if !strings.Contains(content, "Second version.") {
		t.Errorf("Head content = %q, want to contain 'Second version.'", content)
	}
}

func TestGitRepo_PlanPath(t *testing.T) {
	tmp := t.TempDir()
	repo, err := NewGitRepo(tmp)
	if err != nil {
		t.Fatalf("NewGitRepo: %v", err)
	}
	if !strings.HasSuffix(repo.PlanPath(), "plan-history/plan.md") {
		t.Errorf("unexpected PlanPath: %s", repo.PlanPath())
	}
}
