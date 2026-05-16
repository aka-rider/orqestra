package plan

import (
	"os/exec"
	"strings"
	"testing"
	"time"
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

func TestGitRepo_CommitPlanAndDialog(t *testing.T) {
	repo, err := NewGitRepo(t.TempDir())
	if err != nil {
		t.Fatalf("NewGitRepo: %v", err)
	}
	entry := DialogEntry{
		Timestamp: time.Now(),
		Role:      "architect",
		Message:   "initial plan created",
	}
	if err := repo.CommitPlanAndDialog("# Plan v1", entry); err != nil {
		t.Fatalf("CommitPlanAndDialog: %v", err)
	}
	content, err := repo.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if content != "# Plan v1" {
		t.Errorf("plan.md = %q, want %q", content, "# Plan v1")
	}
	dialogOut, err := exec.Command("git", "-C", repo.Dir(), "show", "HEAD:dialog.md").Output()
	if err != nil {
		t.Fatalf("read dialog.md: %v", err)
	}
	if !strings.Contains(string(dialogOut), "initial plan created") {
		t.Errorf("dialog.md missing entry, got:\n%s", dialogOut)
	}
	logOut, err := exec.Command("git", "-C", repo.Dir(), "log", "--oneline").Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(logOut)), "\n")
	if len(lines) != 1 {
		t.Errorf("expected 1 commit, got %d: %v", len(lines), lines)
	}
}

func TestGitRepo_DialogOnlyCommit_NoPlanChange(t *testing.T) {
	repo, err := NewGitRepo(t.TempDir())
	if err != nil {
		t.Fatalf("NewGitRepo: %v", err)
	}
	entry1 := DialogEntry{Timestamp: time.Now(), Role: "architect", Message: "plan created"}
	if err := repo.CommitPlanAndDialog("plan", entry1); err != nil {
		t.Fatal(err)
	}
	entry2 := DialogEntry{Timestamp: time.Now(), Role: "critic", Message: "feedback"}
	if err := repo.CommitDialog(entry2); err != nil {
		t.Fatal(err)
	}

	planDiff, err := exec.Command("git", "-C", repo.Dir(), "diff", "HEAD~1", "HEAD", "--", "plan.md").Output()
	if err != nil {
		t.Fatalf("git diff plan.md: %v", err)
	}
	if len(strings.TrimSpace(string(planDiff))) != 0 {
		t.Errorf("expected no plan.md diff, got:\n%s", planDiff)
	}

	dialogDiff, err := exec.Command("git", "-C", repo.Dir(), "diff", "HEAD~1", "HEAD", "--", "dialog.md").Output()
	if err != nil {
		t.Fatalf("git diff dialog.md: %v", err)
	}
	if len(strings.TrimSpace(string(dialogDiff))) == 0 {
		t.Error("expected dialog.md diff to be non-empty")
	}

	planLog, err := exec.Command("git", "-C", repo.Dir(), "log", "--oneline", "--", "plan.md").Output()
	if err != nil {
		t.Fatalf("git log plan.md: %v", err)
	}
	planCommits := strings.Split(strings.TrimSpace(string(planLog)), "\n")
	if len(planCommits) != 1 {
		t.Errorf("expected 1 plan.md commit, got %d", len(planCommits))
	}

	allLog, err := exec.Command("git", "-C", repo.Dir(), "log", "--oneline").Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	allCommits := strings.Split(strings.TrimSpace(string(allLog)), "\n")
	if len(allCommits) != 2 {
		t.Errorf("expected 2 total commits, got %d", len(allCommits))
	}
}

func TestGitRepo_PlanRevision_UpdatesBothFiles(t *testing.T) {
	repo, err := NewGitRepo(t.TempDir())
	if err != nil {
		t.Fatalf("NewGitRepo: %v", err)
	}
	entry1 := DialogEntry{Timestamp: time.Now(), Role: "architect", Message: "v1 plan"}
	if err := repo.CommitPlanAndDialog("v1", entry1); err != nil {
		t.Fatal(err)
	}
	entry2 := DialogEntry{Timestamp: time.Now(), Role: "architect", Message: "v2 plan"}
	if err := repo.CommitPlanAndDialog("v2", entry2); err != nil {
		t.Fatal(err)
	}

	planDiff, err := exec.Command("git", "-C", repo.Dir(), "diff", "HEAD~1", "HEAD", "--", "plan.md").Output()
	if err != nil {
		t.Fatal(err)
	}
	diffStr := string(planDiff)
	if !strings.Contains(diffStr, "v1") || !strings.Contains(diffStr, "v2") {
		t.Errorf("plan.md diff should contain v1 and v2, got:\n%s", diffStr)
	}

	dialogDiff, err := exec.Command("git", "-C", repo.Dir(), "diff", "HEAD~1", "HEAD", "--", "dialog.md").Output()
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(string(dialogDiff))) == 0 {
		t.Error("expected dialog.md diff to be non-empty")
	}
}

func TestGitRepo_DialogAppendOnly(t *testing.T) {
	repo, err := NewGitRepo(t.TempDir())
	if err != nil {
		t.Fatalf("NewGitRepo: %v", err)
	}
	messages := []string{"first message", "second message", "third message"}
	for _, msg := range messages {
		entry := DialogEntry{Timestamp: time.Now(), Role: "architect", Message: msg}
		if err := repo.CommitDialog(entry); err != nil {
			t.Fatalf("CommitDialog(%q): %v", msg, err)
		}
	}
	dialogOut, err := exec.Command("git", "-C", repo.Dir(), "show", "HEAD:dialog.md").Output()
	if err != nil {
		t.Fatalf("read dialog.md: %v", err)
	}
	dialog := string(dialogOut)
	for _, msg := range messages {
		if !strings.Contains(dialog, msg) {
			t.Errorf("dialog.md missing %q", msg)
		}
	}
	if count := strings.Count(dialog, "---"); count != 3 {
		t.Errorf("expected 3 separators, got %d", count)
	}
}

func TestGitRepo_FullConversation(t *testing.T) {
	repo, err := NewGitRepo(t.TempDir())
	if err != nil {
		t.Fatalf("NewGitRepo: %v", err)
	}

	// Step 1
	if err := repo.CommitPlanAndDialog("initial", DialogEntry{Timestamp: time.Now(), Role: "architect", Message: "initial plan"}); err != nil {
		t.Fatal(err)
	}
	// Step 2
	if err := repo.CommitDialog(DialogEntry{Timestamp: time.Now(), Role: "critic", Message: "3 blockers found"}); err != nil {
		t.Fatal(err)
	}
	// Step 3
	if err := repo.CommitPlanAndDialog("revised", DialogEntry{Timestamp: time.Now(), Role: "architect", Message: "Re: critic feedback"}); err != nil {
		t.Fatal(err)
	}
	// Step 4
	if err := repo.CommitDialog(DialogEntry{Timestamp: time.Now(), Role: "user", Message: "fix WP1"}); err != nil {
		t.Fatal(err)
	}
	// Step 5
	if err := repo.CommitPlanAndDialog("revised2", DialogEntry{Timestamp: time.Now(), Role: "architect", Message: "Re: fix WP1"}); err != nil {
		t.Fatal(err)
	}
	// Step 6
	if err := repo.CommitDialog(DialogEntry{Timestamp: time.Now(), Role: "user", Message: "looks good"}); err != nil {
		t.Fatal(err)
	}
	// Step 7
	if err := repo.CommitDialog(DialogEntry{Timestamp: time.Now(), Role: "architect", Message: "Re: looks good (chat only)"}); err != nil {
		t.Fatal(err)
	}

	// Assert 7 total commits
	allLog, err := exec.Command("git", "-C", repo.Dir(), "log", "--oneline").Output()
	if err != nil {
		t.Fatal(err)
	}
	allCommits := strings.Split(strings.TrimSpace(string(allLog)), "\n")
	if len(allCommits) != 7 {
		t.Errorf("expected 7 total commits, got %d", len(allCommits))
	}

	// Assert 3 plan.md commits
	planLog, err := exec.Command("git", "-C", repo.Dir(), "log", "--oneline", "--", "plan.md").Output()
	if err != nil {
		t.Fatal(err)
	}
	planCommits := strings.Split(strings.TrimSpace(string(planLog)), "\n")
	if len(planCommits) != 3 {
		t.Errorf("expected 3 plan.md commits, got %d", len(planCommits))
	}

	// Assert dialog.md contains 7 entries
	dialogOut, err := exec.Command("git", "-C", repo.Dir(), "show", "HEAD:dialog.md").Output()
	if err != nil {
		t.Fatal(err)
	}
	dialog := string(dialogOut)
	if count := strings.Count(dialog, "---"); count != 7 {
		t.Errorf("expected 7 dialog entries, got %d separators", count)
	}

	// Assert plan.md = "revised2"
	content, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	if content != "revised2" {
		t.Errorf("plan.md = %q, want %q", content, "revised2")
	}
}

func TestGitRepo_DiffPlain_NoColor(t *testing.T) {
	repo, err := NewGitRepo(t.TempDir())
	if err != nil {
		t.Fatalf("NewGitRepo: %v", err)
	}
	entry1 := DialogEntry{Timestamp: time.Now(), Role: "architect", Message: "v1"}
	if err := repo.CommitPlanAndDialog("v1", entry1); err != nil {
		t.Fatal(err)
	}
	hash, err := repo.HeadCommitHash()
	if err != nil {
		t.Fatalf("HeadCommitHash: %v", err)
	}
	entry2 := DialogEntry{Timestamp: time.Now(), Role: "architect", Message: "v2"}
	if err := repo.CommitPlanAndDialog("v2", entry2); err != nil {
		t.Fatal(err)
	}
	diff, err := repo.DiffPlain(hash)
	if err != nil {
		t.Fatalf("DiffPlain: %v", err)
	}
	if !strings.Contains(diff, "---") || !strings.Contains(diff, "+++") {
		t.Errorf("diff should contain --- and +++ markers, got:\n%s", diff)
	}
	if strings.Contains(diff, "\x1b[") {
		t.Error("diff should not contain ANSI escape sequences")
	}
}

func TestGitRepo_CommitPlan_UserEdit(t *testing.T) {
	repo, err := NewGitRepo(t.TempDir())
	if err != nil {
		t.Fatalf("NewGitRepo: %v", err)
	}
	entry1 := DialogEntry{Timestamp: time.Now(), Role: "architect", Message: "initial"}
	if err := repo.CommitPlanAndDialog("v1", entry1); err != nil {
		t.Fatal(err)
	}
	if err := repo.CommitPlan("v2", "user: manual edit"); err != nil {
		t.Fatal(err)
	}
	dialogOut, err := exec.Command("git", "-C", repo.Dir(), "show", "HEAD:dialog.md").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dialogOut), "(see plan.md diff)") {
		t.Errorf("dialog.md should contain '(see plan.md diff)', got:\n%s", dialogOut)
	}
	content, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	if content != "v2" {
		t.Errorf("plan.md = %q, want %q", content, "v2")
	}
}
