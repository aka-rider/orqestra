package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/testutil"
)

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

func TestSessionDir_SubDir(t *testing.T) {
	s := SessionDir{Path: "/tmp/session"}
	tests := []struct {
		name string
		want string
	}{
		{"research", "/tmp/session/research"},
		{"deliberation", "/tmp/session/deliberation"},
		{"execution", "/tmp/session/execution"},
		{"validation", "/tmp/session/validation"},
		{"deep/nested", "/tmp/session/deep/nested"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.SubDir(tt.name); got != tt.want {
				t.Errorf("SubDir(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestSessionDir_PhaseDirs(t *testing.T) {
	s := SessionDir{Path: "/tmp/session"}
	if got := s.ResearchDir(); got != "/tmp/session/research" {
		t.Errorf("ResearchDir() = %q, want %q", got, "/tmp/session/research")
	}
	if got := s.DeliberationDir(); got != "/tmp/session/deliberation" {
		t.Errorf("DeliberationDir() = %q, want %q", got, "/tmp/session/deliberation")
	}
	if got := s.ExecutionDir(); got != "/tmp/session/execution" {
		t.Errorf("ExecutionDir() = %q, want %q", got, "/tmp/session/execution")
	}
	if got := s.ValidationDir(); got != "/tmp/session/validation" {
		t.Errorf("ValidationDir() = %q, want %q", got, "/tmp/session/validation")
	}
}

func TestMkdir(t *testing.T) {
	tmp := t.TempDir()

	err := mkdir(filepath.Join(tmp, "new"), 0o755)
	if err != nil {
		t.Fatalf("mkdir new dir: %v", err)
	}

	err = mkdir(filepath.Join(tmp, "new"), 0o755)
	if err == nil {
		t.Fatal("mkdir existing dir: expected error, got nil")
	}

	ro := filepath.Join(tmp, "readonly")
	if err := os.Mkdir(ro, 0o555); err != nil {
		t.Skipf("cannot create readonly dir: %v", err)
	}
	err = mkdir(filepath.Join(ro, "child"), 0o755)
	if err == nil {
		t.Fatal("mkdir in readonly dir: expected error, got nil")
	}
}

func TestMkdirAll(t *testing.T) {
	tmp := t.TempDir()

	path := filepath.Join(tmp, "a", "b", "c")
	err := mkdirAll(path, 0o755)
	if err != nil {
		t.Fatalf("mkdirAll nested: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat nested dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("expected directory")
	}

	err = mkdirAll(path, 0o755)
	if err != nil {
		t.Fatalf("mkdirAll idempotent: %v", err)
	}
}

func TestMkdirAll_NotADir(t *testing.T) {
	tmp := t.TempDir()

	filePath := filepath.Join(tmp, "target")
	if err := os.WriteFile(filePath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	err := mkdirAll(filePath, 0o755)
	if err == nil {
		t.Fatal("mkdirAll on file: expected error, got nil")
	}
}
