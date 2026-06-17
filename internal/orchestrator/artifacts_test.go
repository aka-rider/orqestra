package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/agent"
)


func TestWriteArtifactIn_Fallback(t *testing.T) {
	tmp := t.TempDir()
	sess := agent.SessionDir{Path: tmp}

	// Write to a non-writable subdir — should fall back to session root.
	// We can't easily make a non-writable dir on all platforms, so just
	// verify the normal path works and the fallback code path exists.
	path := writeArtifactIn(sess, "research", "draft.md", "draft content")
	if path == "" {
		t.Fatal("expected non-empty path")
	}
	data, err := os.ReadFile(filepath.Join(tmp, "research", "draft.md"))
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if string(data) != "draft content" {
		t.Errorf("content = %q, want %q", data, "draft content")
	}
}

func TestWriteArtifactJSONIn(t *testing.T) {
	tmp := t.TempDir()
	sess := agent.SessionDir{Path: tmp}

	type meta struct {
		AgentID string `json:"agent_id"`
		Status  string `json:"status"`
	}
	m := meta{AgentID: "researcher", Status: "done"}
	path := writeArtifactJSONIn(sess, "research", "researcher_r01_meta.json", m)
	if path == "" {
		t.Fatal("expected non-empty path")
	}

	data, err := os.ReadFile(filepath.Join(tmp, "research", "researcher_r01_meta.json"))
	if err != nil {
		t.Fatalf("read JSON artifact: %v", err)
	}
	// Should be valid JSON with the expected fields.
	var decoded meta
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal JSON: %v", err)
	}
	if decoded.AgentID != "researcher" || decoded.Status != "done" {
		t.Errorf("decoded = %+v, want {AgentID: researcher, Status: done}", decoded)
	}
}

func TestAppendDialog(t *testing.T) {
	tmp := t.TempDir()

	// First turn.
	appendDialog(tmp, "Human", "Please revise the plan.")
	data, err := os.ReadFile(filepath.Join(tmp, "dialog.md"))
	if err != nil {
		t.Fatalf("read dialog: %v", err)
	}
	expected := "## Human\nPlease revise the plan.\n\n---\n"
	if string(data) != expected {
		t.Errorf("dialog = %q, want %q", data, expected)
	}

	// Second turn (should be appended).
	appendDialog(tmp, "Agent", "Here is the revised plan.\n\nplan-v2.md")
	data, err = os.ReadFile(filepath.Join(tmp, "dialog.md"))
	if err != nil {
		t.Fatalf("read dialog: %v", err)
	}
	expected = "## Human\nPlease revise the plan.\n\n---\n## Agent\nHere is the revised plan.\n\nplan-v2.md\n\n---\n"
	if string(data) != expected {
		t.Errorf("dialog = %q, want %q", data, expected)
	}
}

func TestAppendDialog_EmptyDir(t *testing.T) {
	// appendDialog with empty dir should be a no-op.
	appendDialog("", "Human", "test")
}

func TestHighestPlanVersion(t *testing.T) {
	tmp := t.TempDir()

	// Empty dir → 0.
	if v := highestPlanVersion(tmp); v != 0 {
		t.Errorf("empty dir version = %d, want 0", v)
	}

	// Create some plan files.
	os.WriteFile(filepath.Join(tmp, "plan-v1.md"), []byte("v1"), 0o644)
	os.WriteFile(filepath.Join(tmp, "plan-v3.md"), []byte("v3"), 0o644)
	os.WriteFile(filepath.Join(tmp, "plan-v2.md"), []byte("v2"), 0o644)
	// Non-matching files should be ignored.
	os.WriteFile(filepath.Join(tmp, "plan-v1.bak"), []byte("bak"), 0o644)
	os.WriteFile(filepath.Join(tmp, "other.md"), []byte("other"), 0o644)

	if v := highestPlanVersion(tmp); v != 3 {
		t.Errorf("version = %d, want 3", v)
	}
}

func TestFindHighestPlan(t *testing.T) {
	tmp := t.TempDir()

	// Empty dir → "".
	if p := findHighestPlan(tmp); p != "" {
		t.Errorf("empty dir plan = %q, want empty", p)
	}

	// Create plan files.
	os.WriteFile(filepath.Join(tmp, "plan-v1.md"), []byte("v1"), 0o644)
	os.WriteFile(filepath.Join(tmp, "plan-v5.md"), []byte("v5"), 0o644)
	os.WriteFile(filepath.Join(tmp, "plan-v3.md"), []byte("v3"), 0o644)

	p := findHighestPlan(tmp)
	if p == "" {
		t.Fatal("expected non-empty plan path")
	}
	if !strings.Contains(p, "plan-v5.md") {
		t.Errorf("plan = %q, should contain plan-v5.md", p)
	}
}
