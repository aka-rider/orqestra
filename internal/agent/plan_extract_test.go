package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
)

// setupPlanFile creates a fake Claude CLI plan file and session JSONL for testing.
// It sets HOME to a temp directory, creates ~/.claude/plans/<sessionID>-plan.md
// with the given content, and creates the session JSONL with a plan_mode attachment.
// Returns the plan file path for reference.
func setupPlanFile(t *testing.T, sessionID, planContent string) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Create plan file
	plansDir := filepath.Join(tmp, ".claude", "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}
	planFile := filepath.Join(plansDir, sessionID+"-plan.md")
	if err := os.WriteFile(planFile, []byte(planContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create session JSONL referencing the plan file
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	projDir := filepath.Join(tmp, ".claude", "projects", harness.CwdToDash(resolved))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(projDir, sessionID+".jsonl")
	jsonlContent := fmt.Sprintf(`{"type":"attachment","attachment":{"type":"plan_mode","planFilePath":%q}}`, planFile)
	if err := os.WriteFile(jsonlPath, []byte(jsonlContent+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	return planFile
}

func TestReadPlanFile_NoSessionID(t *testing.T) {
	// INV-P1-PLANSRC: missing session ID is an integrity error, not a default
	_, err := ReadPlanFile("", "", "")
	if err == nil {
		t.Fatal("expected error for missing session ID")
	}
	if got := err.Error(); got != "no session ID" {
		t.Errorf("unexpected error: %s", got)
	}
}

func TestReadPlanFile_MissingJSONL(t *testing.T) {
	// INV-P1-PLANSRC: missing session JSONL is an integrity error
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	_, err = ReadPlanFile("nonexistent-session", "", cwd)
	if err == nil {
		t.Fatal("expected error for missing JSONL")
	}
}

// Gate for INV-P1-PLANSRC: a plan path outside ~/.claude/plans/ is rejected.
func TestReadPlanFile_SecurityGate(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	sessionID := "test-security"

	// Create plan file OUTSIDE ~/.claude/plans/
	evilDir := filepath.Join(tmp, "evil")
	if err := os.MkdirAll(evilDir, 0o755); err != nil {
		t.Fatal(err)
	}
	evilFile := filepath.Join(evilDir, "stolen.md")
	if err := os.WriteFile(evilFile, []byte("# Plan\n\n## Work Packages\n..."), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create session JSONL pointing to the evil path
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	projDir := filepath.Join(tmp, ".claude", "projects", harness.CwdToDash(resolved))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(projDir, sessionID+".jsonl")
	jsonlContent := fmt.Sprintf(`{"type":"attachment","attachment":{"type":"plan_mode","planFilePath":%q}}`, evilFile)
	if err := os.WriteFile(jsonlPath, []byte(jsonlContent+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = ReadPlanFile(sessionID, "", cwd)
	if err == nil {
		t.Fatal("expected error for out-of-bounds plan file")
	}
}

// TestReadPlanFile_SymlinkEscapeRejected verifies that a plan file path which is
// lexically inside ~/.claude/plans/ but is actually a symlink pointing
// OUTSIDE that directory is rejected. Before the fix, readSecurePlanFile used
// filepath.Abs + strings.HasPrefix — a purely lexical check that a symlink
// defeats trivially: the string still starts with the allowed prefix even
// though the resolved target is elsewhere.
func TestReadPlanFile_SymlinkEscapeRejected(t *testing.T) {
	// INV-P1-PLANSRC: a symlink inside the plans dir pointing outside it must be rejected.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	sessionID := "test-symlink-escape"

	// Secret file OUTSIDE ~/.claude/plans/.
	secretDir := filepath.Join(tmp, "secret")
	if err := os.MkdirAll(secretDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secretFile := filepath.Join(secretDir, "stolen.md")
	if err := os.WriteFile(secretFile, []byte("# Plan\n\n## Work Packages\nsecret content"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Symlink INSIDE ~/.claude/plans/ pointing at the secret file.
	plansDir := filepath.Join(tmp, ".claude", "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(plansDir, sessionID+"-plan.md")
	if err := os.Symlink(secretFile, linkPath); err != nil {
		t.Fatal(err)
	}

	// Provide the symlink path directly via the stream-captured planFilePath —
	// lexically it is under ~/.claude/plans/, but its resolved target is not.
	_, err := ReadPlanFile(sessionID, linkPath, "")
	if err == nil {
		t.Fatal("expected error for a plan file symlink escaping ~/.claude/plans/")
	}
}

func TestReadPlanFile_EmptyPlanFile(t *testing.T) {
	// INV-P1-PLANSRC: empty plan file is an integrity error, not a valid empty plan
	sessionID := "test-empty"
	setupPlanFile(t, sessionID, "")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	_, err = ReadPlanFile(sessionID, "", cwd)
	if err == nil {
		t.Fatal("expected error for empty plan file")
	}
}

func TestReadPlanFile_UsesStreamPlanFilePath(t *testing.T) {
	// INV-P1-PLANSRC: plan is read from planFilePath when provided directly
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Create plan file under ~/.claude/plans/ directly
	plansDir := filepath.Join(tmp, ".claude", "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}
	planFile := filepath.Join(plansDir, "stream-plan.md")
	planMD := "# Plan\n\n## Goal\nStream test.\n\n## Work Packages\n..."
	if err := os.WriteFile(planFile, []byte(planMD), 0o644); err != nil {
		t.Fatal(err)
	}

	// Provide planFilePath directly — repoCWD is not needed for this path.
	content, err := ReadPlanFile("some-session", planFile, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != planMD {
		t.Errorf("content mismatch:\ngot:  %s\nwant: %s", content, planMD)
	}
}

func TestReadPlanFile_PlanFileNeverWritten(t *testing.T) {
	// INV-P1-PLANSRC: agent that received no plan_mode attachment → integrity error
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	sessionID := "test-never-written"

	// Create the plans directory but do NOT create the plan file.
	plansDir := filepath.Join(tmp, ".claude", "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ghostPlan := filepath.Join(plansDir, "ghost-plan.md")

	// Create session JSONL with a plan_mode attachment pointing to the missing file.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	projDir := filepath.Join(tmp, ".claude", "projects", harness.CwdToDash(resolved))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(projDir, sessionID+".jsonl")
	jsonlContent := fmt.Sprintf(`{"type":"attachment","attachment":{"type":"plan_mode","planFilePath":%q}}`, ghostPlan)
	if err := os.WriteFile(jsonlPath, []byte(jsonlContent+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = ReadPlanFile(sessionID, "", cwd)
	if err == nil {
		t.Fatal("expected error for plan file that was never written")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, sessionID) {
		t.Errorf("expected session ID in error, got: %s", errMsg)
	}
}

// setupGhostPlan creates the plans dir and session JSONL with a plan_mode attachment
// pointing to a non-existent plan file. Additional JSONL lines can be appended via extraLines.
func setupGhostPlan(t *testing.T, sessionID string, extraLines ...string) (ghostPlan, cwd string) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	plansDir := filepath.Join(tmp, ".claude", "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ghostPlan = filepath.Join(plansDir, sessionID+"-ghost.md")

	var err error
	cwd, err = os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	projDir := filepath.Join(tmp, ".claude", "projects", harness.CwdToDash(resolved))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(projDir, sessionID+".jsonl")
	content := fmt.Sprintf(`{"type":"attachment","attachment":{"type":"plan_mode","planFilePath":%q}}`, ghostPlan) + "\n"
	for _, line := range extraLines {
		content += line + "\n"
	}
	if err := os.WriteFile(jsonlPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return ghostPlan, cwd
}

// TestReadPlanFile_NeverUsesJSONLTextFallback verifies that ReadPlanFile —
// the strict function used at orchestrator integrity boundaries — NEVER
// recovers plan content from the session JSONL's final assistant message,
// even when that message is present and well-formed. Root CLAUDE.md §5.1:
// "NEVER scrape a plan from stdout." Unlike the deleted ReadPlan (which had a
// withFallback tier-4 for this), ReadPlanFile has exactly one behavior here:
// fail closed when the plan file was never written.
func TestResolvePlanFilePath_NoSessionID(t *testing.T) {
	_, err := ResolvePlanFilePath("", "", "")
	if err == nil {
		t.Fatal("expected error for missing session ID")
	}
}

func TestResolvePlanFilePath_ReturnsDirectPathWithoutTouchingDisk(t *testing.T) {
	// ResolvePlanFilePath never reads or security-checks the path — it just
	// echoes it back when one was supplied directly, even for a path that
	// does not exist on disk (that is the caller's problem, e.g. os.Stat).
	got, err := ResolvePlanFilePath("some-session", "/does/not/exist/plan.md", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/does/not/exist/plan.md" {
		t.Errorf("got %q, want the path echoed back unchanged", got)
	}
}

func TestResolvePlanFilePath_FallsBackToJSONLAttachment(t *testing.T) {
	ghostPlan, cwd := setupGhostPlan(t, "test-resolve-fallback")
	got, err := ResolvePlanFilePath("test-resolve-fallback", "", cwd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != ghostPlan {
		t.Errorf("got %q, want %q (from the JSONL plan_mode attachment)", got, ghostPlan)
	}
}

func TestResolvePlanFilePath_NoJSONLIsAnError(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	_, err = ResolvePlanFilePath("nonexistent-session", "", cwd)
	if err == nil {
		t.Fatal("expected error when no JSONL session log exists")
	}
}

func TestReadPlanFile_NeverUsesJSONLTextFallback(t *testing.T) {
	wantText := "## Plan\nContent that should not be recovered."
	textLine := fmt.Sprintf(`{"type":"assistant","message":{"stop_reason":"end_turn","content":[{"type":"text","text":%q}]}}`, wantText)
	_, cwd := setupGhostPlan(t, "test-fallback-disabled", textLine)

	content, err := ReadPlanFile("test-fallback-disabled", "", cwd)
	if err == nil {
		t.Fatalf("expected error when plan file not written; got content: %q", content)
	}
	if content != "" {
		t.Errorf("expected no content recovered from JSONL fallback, got: %q", content)
	}
}
