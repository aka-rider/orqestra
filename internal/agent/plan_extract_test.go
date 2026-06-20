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

func TestReadPlanFromRun_Success(t *testing.T) {
	sessionID := "test-extract-success"
	planMD := "# Plan\n\n## Goal\nDo something.\n\n## Work Packages\n\n### 1. Do stuff"
	setupPlanFile(t, sessionID, planMD)

	result := harness.RunResult{SessionID: sessionID}
	content, err := ReadPlanFromRun(result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != planMD {
		t.Errorf("content mismatch:\ngot:  %s\nwant: %s", content, planMD)
	}
}

func TestReadPlanFromRun_NoSessionID(t *testing.T) {
	result := harness.RunResult{}
	_, err := ReadPlanFromRun(result)
	if err == nil {
		t.Fatal("expected error for missing session ID")
	}
	if got := err.Error(); got != "no session ID in run result" {
		t.Errorf("unexpected error: %s", got)
	}
}

func TestReadPlanFromRun_MissingJSONL(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	result := harness.RunResult{SessionID: "nonexistent-session"}
	_, err := ReadPlanFromRun(result)
	if err == nil {
		t.Fatal("expected error for missing JSONL")
	}
}

func TestReadPlanFromRun_SecurityGate(t *testing.T) {
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

	result := harness.RunResult{SessionID: sessionID}
	_, err = ReadPlanFromRun(result)
	if err == nil {
		t.Fatal("expected error for out-of-bounds plan file")
	}
}

func TestReadPlanFromRun_EmptyPlanFile(t *testing.T) {
	sessionID := "test-empty"
	setupPlanFile(t, sessionID, "")

	result := harness.RunResult{SessionID: sessionID}
	_, err := ReadPlanFromRun(result)
	if err == nil {
		t.Fatal("expected error for empty plan file")
	}
}

func TestReadPlanFromRun_PlanFileNeverWritten(t *testing.T) {
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

	result := harness.RunResult{SessionID: sessionID}
	_, err = ReadPlanFromRun(result)
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
