package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
)

// RepoRoot walks upward from the test's working directory to find go.mod and
// returns that directory. Fails the test if go.mod is not found.
func RepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found — is this test running inside the module?")
		}
		dir = parent
	}
}

// TranscriptFixturePath returns the path to a file inside testdata/transcripts/<scenario>/.
// Use RepoRoot to locate the module root; the fixture must be committed under testdata/.
func TranscriptFixturePath(t *testing.T, scenario, file string) string {
	t.Helper()
	p := filepath.Join(RepoRoot(t), "testdata", "transcripts", scenario, file)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("transcript fixture missing: %s (err: %v)", p, err)
	}
	return p
}

// MustTempHome sets HOME to a fresh temp dir for the duration of the test.
// Tests calling MustTempHome must NOT call t.Parallel() — HOME is process-wide.
func MustTempHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	return tmp
}

// SetupPlanFile creates a plan file and session JSONL under the current HOME.
// Call MustTempHome before SetupPlanFile.
func SetupPlanFile(t *testing.T, sessionID, planContent string) {
	t.Helper()
	home := os.Getenv("HOME")
	if home == "" || !filepath.IsAbs(home) {
		t.Fatal("HOME not set; call MustTempHome before SetupPlanFile")
	}

	plansDir := filepath.Join(home, ".claude", "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}
	planFile := filepath.Join(plansDir, sessionID+"-plan.md")
	if err := os.WriteFile(planFile, []byte(planContent), 0o644); err != nil {
		t.Fatal(err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	projDir := filepath.Join(home, ".claude", "projects", harness.CwdToDash(resolved))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(projDir, sessionID+".jsonl")
	jsonlContent := fmt.Sprintf(`{"type":"attachment","attachment":{"type":"plan_mode","planFilePath":%q}}`, planFile)
	if err := os.WriteFile(jsonlPath, []byte(jsonlContent+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ValidPlanMarkdown returns a minimal valid plan document for use in tests.
func ValidPlanMarkdown() string {
	return "# Plan\n\n## Goal\nAdd feature X.\n\n## Work Packages\n\n### 1. Add X\n\n**Steps:**\n1. Create pkg/x.go\n\n**Done when:**\n- go test ./pkg passes"
}
