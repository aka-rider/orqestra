package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/testutil"
)

func setupTestSessions(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()

	// Create two session dirs
	sess1 := filepath.Join(tmp, ".orqestra", "sessions", "2026-05-09-100000-first-run")
	sess2 := filepath.Join(tmp, ".orqestra", "sessions", "2026-05-10-120000-second-run")

	for _, dir := range []string{sess1, sess2} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Write artifacts for sess1
	os.WriteFile(filepath.Join(sess1, "prompt.md"), []byte("Add feature X"), 0o644)
	writeMeta(t, sess1, "researcher_meta.json", StepMeta{
		AgentID:   "researcher",
		StartTime: time.Date(2026, 5, 9, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 5, 9, 10, 0, 30, 0, time.UTC),
		Status:    "done",
	})
	writeMeta(t, sess1, "worker_meta.json", StepMeta{
		AgentID:              "worker",
		StartTime:            time.Date(2026, 5, 9, 10, 1, 0, 0, time.UTC),
		EndTime:              time.Date(2026, 5, 9, 10, 5, 0, 0, time.UTC),
		ClaudeSessionID:      "sess-abc",
		ClaudeSessionLogPath: "sessions/worker_session.jsonl",
		Status:               "done",
		InputTokens:          1000,
		OutputTokens:         500,
	})
	os.WriteFile(filepath.Join(sess1, "final_plan.md"), []byte("# Plan\n\nDo stuff"), 0o644)
	os.WriteFile(filepath.Join(sess1, "worker_output.txt"), []byte("done"), 0o644)
	os.WriteFile(filepath.Join(sess1, "worker_validation.txt"), []byte("✓ pass"), 0o644)

	// Write artifacts for sess2 (minimal — no prompt.md to test graceful handling)
	writeMeta(t, sess2, "researcher_meta.json", StepMeta{
		AgentID:   "researcher",
		StartTime: time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 5, 10, 12, 0, 10, 0, time.UTC),
		Status:    "failed",
		Error:     "connection refused",
	})

	return tmp
}

func writeMeta(t *testing.T, dir, name string, meta StepMeta) {
	t.Helper()
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestListRuns_SortedNewestFirst(t *testing.T) {
	repoPath := setupTestSessions(t)

	runs, err := ListRuns(repoPath)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(runs))
	}

	// Newest first
	if runs[0].Slug != "second-run" {
		t.Errorf("expected first run to be 'second-run', got %q", runs[0].Slug)
	}
	if runs[1].Slug != "first-run" {
		t.Errorf("expected second run to be 'first-run', got %q", runs[1].Slug)
	}
}

func TestListRuns_MissingPrompt(t *testing.T) {
	repoPath := setupTestSessions(t)

	runs, err := ListRuns(repoPath)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}

	// sess2 has no prompt.md — should be empty string, not error
	if runs[0].Prompt != "" {
		t.Errorf("expected empty prompt for sess2, got %q", runs[0].Prompt)
	}
	if runs[1].Prompt != "Add feature X" {
		t.Errorf("expected 'Add feature X', got %q", runs[1].Prompt)
	}
}

func TestListRuns_StatusFromLastStep(t *testing.T) {
	repoPath := setupTestSessions(t)

	runs, err := ListRuns(repoPath)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}

	if runs[0].Status != "failed" {
		t.Errorf("expected status 'failed' for sess2, got %q", runs[0].Status)
	}
	if runs[1].Status != "done" {
		t.Errorf("expected status 'done' for sess1, got %q", runs[1].Status)
	}
}

func TestListRuns_NonexistentDir(t *testing.T) {
	runs, err := ListRuns(t.TempDir())
	if err != nil {
		t.Fatalf("expected nil error for nonexistent dir, got: %v", err)
	}
	if runs != nil {
		t.Errorf("expected nil runs, got %d", len(runs))
	}
}

func TestLoadRunDetail_AllFields(t *testing.T) {
	repoPath := setupTestSessions(t)
	sess1Path := filepath.Join(repoPath, ".orqestra", "sessions", "2026-05-09-100000-first-run")

	detail, err := LoadRunDetail(sess1Path)
	if err != nil {
		t.Fatalf("LoadRunDetail: %v", err)
	}

	if detail.Prompt != "Add feature X" {
		t.Errorf("prompt = %q, want 'Add feature X'", detail.Prompt)
	}
	if detail.PlanMarkdown != "# Plan\n\nDo stuff" {
		t.Errorf("plan = %q", detail.PlanMarkdown)
	}
	if detail.WorkerOutput != "done" {
		t.Errorf("worker output = %q", detail.WorkerOutput)
	}
	if detail.Validation != "✓ pass" {
		t.Errorf("validation = %q", detail.Validation)
	}
	if len(detail.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(detail.Steps))
	}
	if detail.Steps[0].AgentID != "researcher" {
		t.Errorf("step 0 agent = %q, want 'researcher'", detail.Steps[0].AgentID)
	}
	if detail.Steps[1].AgentID != "worker" {
		t.Errorf("step 1 agent = %q, want 'worker'", detail.Steps[1].AgentID)
	}
	if detail.Steps[1].ClaudeSessionID != "sess-abc" {
		t.Errorf("step 1 session ID = %q, want 'sess-abc'", detail.Steps[1].ClaudeSessionID)
	}
	if detail.Steps[1].ClaudeSessionLogPath != "sessions/worker_session.jsonl" {
		t.Errorf("step 1 session log path = %q, want 'sessions/worker_session.jsonl'", detail.Steps[1].ClaudeSessionLogPath)
	}
}

func TestLoadRunDetail_Duration(t *testing.T) {
	repoPath := setupTestSessions(t)
	sess1Path := filepath.Join(repoPath, ".orqestra", "sessions", "2026-05-09-100000-first-run")

	detail, err := LoadRunDetail(sess1Path)
	if err != nil {
		t.Fatalf("LoadRunDetail: %v", err)
	}

	// Duration should span researcher start (10:00:00) to worker end (10:05:00) = 5 min
	if detail.Duration != 5*time.Minute {
		t.Errorf("duration = %v, want 5m", detail.Duration)
	}
}

func TestParseSessionDirName(t *testing.T) {
	tests := []struct {
		name     string
		wantSlug string
		wantOK   bool
	}{
		{"2026-05-10-120000", "", true},
		{"2026-05-10-120000-my-run", "my-run", true},
		{"not-a-date", "", false},
		{"short", "", false},
	}

	for _, tt := range tests {
		ts, slug := parseSessionDirName(tt.name)
		if tt.wantOK && ts.IsZero() {
			t.Errorf("parseSessionDirName(%q) returned zero time, want valid", tt.name)
		}
		if !tt.wantOK && !ts.IsZero() {
			t.Errorf("parseSessionDirName(%q) returned valid time, want zero", tt.name)
		}
		if slug != tt.wantSlug {
			t.Errorf("parseSessionDirName(%q) slug = %q, want %q", tt.name, slug, tt.wantSlug)
		}
	}
}

func TestCopySessionLog_EmptySessionID(t *testing.T) {
	tmp := t.TempDir()
	sess, err := NewSessionDir(tmp, "test-run")
	if err != nil {
		t.Fatal(err)
	}
	dest, err := CopySessionLog(sess, tmp, "", "test.jsonl", func(_, _ string) (string, error) { return "", nil })
	if err != nil {
		t.Fatalf("expected nil error for empty sessionID, got: %v", err)
	}
	if dest != "" {
		t.Fatalf("expected empty dest, got %q", dest)
	}
}

func TestCopySessionLog_NotFound(t *testing.T) {
	testutil.MustTempHome(t)
	tmp := t.TempDir()
	sess, err := NewSessionDir(tmp, "test-run")
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dest, err := CopySessionLog(sess, cwd, "nonexistent-session-id", "test.jsonl", harness.ResolveSessionLogPath)
	if err == nil {
		t.Fatal("expected error for nonexistent session ID, got nil")
	}
	if dest != "" {
		t.Fatalf("expected empty dest on error, got %q", dest)
	}
}

func TestCopySessionLog_Success(t *testing.T) {
	testutil.MustTempHome(t)
	sessionID := "copy-test-session"
	testutil.SetupPlanFile(t, sessionID, "# Plan\n\nTest")

	tmp := t.TempDir()
	sess, err := NewSessionDir(tmp, "test-run")
	if err != nil {
		t.Fatal(err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	dest, err := CopySessionLog(sess, cwd, sessionID, "researcher_session.jsonl", harness.ResolveSessionLogPath)
	if err != nil {
		t.Fatalf("CopySessionLog: %v", err)
	}
	if dest == "" {
		t.Fatal("expected non-empty dest path")
	}

	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading copy: %v", err)
	}
	if len(data) == 0 {
		t.Error("copied file is empty")
	}
}
