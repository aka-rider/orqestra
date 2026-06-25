package tui

import (
	"os"
	"testing"
)

func TestPlanTempFile_RoundTrips(t *testing.T) {
	const plan = "# Plan\n\n- step one\n- step two\n"
	path, err := planTempFile(plan)
	if err != nil {
		t.Fatalf("planTempFile: %v", err)
	}
	defer os.Remove(path)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != plan {
		t.Errorf("round-trip mismatch:\n got %q\nwant %q", got, plan)
	}
}

// Open-in-editor must fail closed (an error message, never a silent GUI fallback)
// when no terminal editor is configured — D8 / the user-intent rule.
func TestOpenExternalEditor_FailsClosedWithoutEditor(t *testing.T) {
	t.Setenv("EDITOR", "")
	t.Setenv("VISUAL", "")

	cmd := openExternalEditor("/tmp/whatever.md")
	if cmd == nil {
		t.Fatal("expected a command, got nil")
	}
	msg, ok := cmd().(editorReturnMsg)
	if !ok {
		t.Fatalf("expected editorReturnMsg, got %T", cmd())
	}
	if msg.err == nil {
		t.Error("expected an error when no editor is set, got nil")
	}
}
