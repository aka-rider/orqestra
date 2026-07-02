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


func TestReadPlan_NoSessionID(t *testing.T) {
	// INV-P1-PLANSRC: missing session ID is an integrity error, not a default
	_, _, err := ReadPlan("", "", "", false)
	if err == nil {
		t.Fatal("expected error for missing session ID")
	}
	if got := err.Error(); got != "no session ID" {
		t.Errorf("unexpected error: %s", got)
	}
}

func TestReadPlan_MissingJSONL(t *testing.T) {
	// INV-P1-PLANSRC: missing session JSONL is an integrity error
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = ReadPlan("nonexistent-session", "", cwd, false)
	if err == nil {
		t.Fatal("expected error for missing JSONL")
	}
}

// Gate for INV-P1-PLANSRC: a plan path outside ~/.claude/plans/ is rejected.
func TestReadPlan_SecurityGate(t *testing.T) {
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

	_, _, err = ReadPlan(sessionID, "", cwd, false)
	if err == nil {
		t.Fatal("expected error for out-of-bounds plan file")
	}
}

// TestReadPlan_SymlinkEscapeRejected verifies that a plan file path which is
// lexically inside ~/.claude/plans/ but is actually a symlink pointing
// OUTSIDE that directory is rejected. Before the fix, readSecurePlanFile used
// filepath.Abs + strings.HasPrefix — a purely lexical check that a symlink
// defeats trivially: the string still starts with the allowed prefix even
// though the resolved target is elsewhere.
func TestReadPlan_SymlinkEscapeRejected(t *testing.T) {
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
	_, _, err := ReadPlan(sessionID, linkPath, "", false)
	if err == nil {
		t.Fatal("expected error for a plan file symlink escaping ~/.claude/plans/")
	}
}

func TestReadPlan_EmptyPlanFile(t *testing.T) {
	// INV-P1-PLANSRC: empty plan file is an integrity error, not a valid empty plan
	sessionID := "test-empty"
	setupPlanFile(t, sessionID, "")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = ReadPlan(sessionID, "", cwd, false)
	if err == nil {
		t.Fatal("expected error for empty plan file")
	}
}

func TestReadPlan_UsesStreamPlanFilePath(t *testing.T) {
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
	content, fromFallback, err := ReadPlan("some-session", planFile, "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fromFallback {
		t.Error("expected fromFallback=false for plan read from file")
	}
	if content != planMD {
		t.Errorf("content mismatch:\ngot:  %s\nwant: %s", content, planMD)
	}
}

func TestReadPlan_PlanFileNeverWritten(t *testing.T) {
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

	_, _, err = ReadPlan(sessionID, "", cwd, false)
	if err == nil {
		t.Fatal("expected error for plan file that was never written")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "did not write a plan file") {
		t.Errorf("expected diagnostic about missing plan file, got: %s", errMsg)
	}
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

func TestReadPlan_JSONLTextFallback(t *testing.T) {
	// INV-P1-PLANSRC: when plan file unwritten, text content from JSONL used as fallback
	wantText := "## Goal\nAdd feature X.\n\n## Codebase Facts\n- file: foo.go"
	textLine := fmt.Sprintf(`{"type":"assistant","message":{"stop_reason":"end_turn","content":[{"type":"text","text":%q}]}}`, wantText)
	_, cwd := setupGhostPlan(t, "test-text-fallback", textLine)

	content, fromFallback, err := ReadPlan("test-text-fallback", "", cwd, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fromFallback {
		t.Error("expected fromFallback=true when model did not write plan file")
	}
	if content != wantText {
		t.Errorf("content mismatch:\ngot:  %s\nwant: %s", content, wantText)
	}
}

func TestReadPlan_JSONLThinkingFallback(t *testing.T) {
	// INV-P1-PLANSRC: thinking content used as fallback when text absent
	wantThinking := "Key findings:\n1. screen_prompt.go uses textarea.Model\n2. clipboard dep exists"
	thinkingLine := fmt.Sprintf(`{"type":"assistant","message":{"stop_reason":"end_turn","content":[{"type":"thinking","thinking":%q}]}}`, wantThinking)
	_, cwd := setupGhostPlan(t, "test-thinking-fallback", thinkingLine)

	content, fromFallback, err := ReadPlan("test-thinking-fallback", "", cwd, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fromFallback {
		t.Error("expected fromFallback=true for thinking-only response")
	}
	if content != wantThinking {
		t.Errorf("content mismatch:\ngot:  %s\nwant: %s", content, wantThinking)
	}
}

func TestReadPlan_JSONLTextBeforeThinking(t *testing.T) {
	// INV-P1-PLANSRC: text content takes priority over thinking in fallback
	wantText := "## Plan\nThe text output."
	mixedLine := fmt.Sprintf(`{"type":"assistant","message":{"stop_reason":"end_turn","content":[{"type":"thinking","thinking":"raw internal deliberation"},{"type":"text","text":%q}]}}`, wantText)
	_, cwd := setupGhostPlan(t, "test-text-priority", mixedLine)

	content, fromFallback, err := ReadPlan("test-text-priority", "", cwd, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fromFallback {
		t.Error("expected fromFallback=true")
	}
	if content != wantText {
		t.Errorf("text should take priority over thinking:\ngot:  %s\nwant: %s", content, wantText)
	}
}

func TestReadPlan_JSONLFallbackNoContent(t *testing.T) {
	// INV-P1-PLANSRC: no end_turn and no plan file → integrity error (not silent empty)
	_, cwd := setupGhostPlan(t, "test-fallback-empty")

	_, _, err := ReadPlan("test-fallback-empty", "", cwd, true)
	if err == nil {
		t.Fatal("expected error when plan file missing and JSONL has no end_turn message")
	}
	if !strings.Contains(err.Error(), "did not write a plan file") {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func TestReadPlan_FallbackDisabled(t *testing.T) {
	// INV-P1-PLANSRC: withFallback=false rejects JSONL content — plan file is required
	wantText := "## Plan\nContent that should not be recovered."
	textLine := fmt.Sprintf(`{"type":"assistant","message":{"stop_reason":"end_turn","content":[{"type":"text","text":%q}]}}`, wantText)
	_, cwd := setupGhostPlan(t, "test-fallback-disabled", textLine)

	// withFallback=false — even though JSONL has content, it should not be used.
	_, _, err := ReadPlan("test-fallback-disabled", "", cwd, false)
	if err == nil {
		t.Fatal("expected error when withFallback=false and plan file not written")
	}
	if !strings.Contains(err.Error(), "did not write a plan file") {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}
