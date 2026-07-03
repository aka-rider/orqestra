package rundir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreate_WrapsSessionDirSemantics(t *testing.T) {
	root := t.TempDir()
	dir, err := Create(root, "my-slug")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if dir.Path == "" {
		t.Fatal("Create returned an empty Path")
	}
	if !strings.Contains(dir.Path, "my-slug") {
		t.Errorf("Create Path %q does not contain the slug", dir.Path)
	}
	wantParent := filepath.Join(root, ".orqestra", "sessions")
	if filepath.Dir(dir.Path) != wantParent {
		t.Errorf("Create Path parent = %q, want %q", filepath.Dir(dir.Path), wantParent)
	}
	info, statErr := os.Stat(dir.Path)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("Create did not leave a directory at %q: %v", dir.Path, statErr)
	}
}

func TestSaveLoadArtifact_RoundTrip(t *testing.T) {
	dir := Dir{Path: t.TempDir()}

	if err := dir.SaveArtifact("note.txt", []byte("hello world")); err != nil {
		t.Fatalf("SaveArtifact: %v", err)
	}
	content, present, err := dir.LoadArtifact("note.txt")
	if err != nil {
		t.Fatalf("LoadArtifact: %v", err)
	}
	if !present {
		t.Error("LoadArtifact: present = false, want true")
	}
	if content != "hello world" {
		t.Errorf("LoadArtifact content = %q, want %q", content, "hello world")
	}
}

func TestLoadArtifact_AbsentIsNotAnError(t *testing.T) {
	dir := Dir{Path: t.TempDir()}

	content, present, err := dir.LoadArtifact("never_written.txt")
	if err != nil {
		t.Fatalf("LoadArtifact on a missing file returned an error: %v", err)
	}
	if present {
		t.Error("LoadArtifact: present = true for a file that was never written")
	}
	if content != "" {
		t.Errorf("LoadArtifact content = %q, want empty", content)
	}
}

func TestLoadArtifact_GenuineReadFailureIsReported(t *testing.T) {
	dir := Dir{Path: t.TempDir()}
	// A directory in place of the expected file makes os.ReadFile fail with
	// something other than IsNotExist — this must surface as an error, not
	// silently collapse to "absent" (§1.1: no swallowed errors).
	if err := os.Mkdir(filepath.Join(dir.Path, "not_a_file.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, present, err := dir.LoadArtifact("not_a_file.txt")
	if err == nil {
		t.Fatal("expected a genuine read error for a directory masquerading as the artifact, got nil")
	}
	if present {
		t.Error("present = true on a genuine read failure, want false")
	}
}

func TestSaveArtifact_EmptyPathFailsClosed(t *testing.T) {
	var dir Dir // zero value: Path == ""
	if err := dir.SaveArtifact("x.txt", []byte("x")); err == nil {
		t.Fatal("expected SaveArtifact on an unset Dir to return an error, got nil")
	}
}

// TestTypedAccessors_RoundTrip proves the well-known artifact accessors
// (prompt/plan/worker output/validation) round-trip byte-identically — the
// shape every run detail view (ListRuns/LoadRunDetail) depends on.
func TestTypedAccessors_RoundTrip(t *testing.T) {
	dir := Dir{Path: t.TempDir()}

	const (
		prompt     = "do the thing\nwith newlines"
		plan       = "# Plan\n\nDo the thing."
		workOutput = "worker finished: created foo.go"
		validation = "VALIDATION: PASS\nAll good."
	)

	if err := dir.SavePrompt(prompt); err != nil {
		t.Fatalf("SavePrompt: %v", err)
	}
	if err := dir.SaveFinalPlan(plan); err != nil {
		t.Fatalf("SaveFinalPlan: %v", err)
	}
	if err := dir.SaveWorkerOutput(workOutput); err != nil {
		t.Fatalf("SaveWorkerOutput: %v", err)
	}
	if err := dir.SaveValidation(validation); err != nil {
		t.Fatalf("SaveValidation: %v", err)
	}

	gotPrompt, err := dir.LoadPrompt()
	if err != nil || gotPrompt != prompt {
		t.Errorf("LoadPrompt = (%q, %v), want (%q, nil)", gotPrompt, err, prompt)
	}
	gotPlan, err := dir.LoadFinalPlan()
	if err != nil || gotPlan != plan {
		t.Errorf("LoadFinalPlan = (%q, %v), want (%q, nil)", gotPlan, err, plan)
	}
	gotOutput, err := dir.LoadWorkerOutput()
	if err != nil || gotOutput != workOutput {
		t.Errorf("LoadWorkerOutput = (%q, %v), want (%q, nil)", gotOutput, err, workOutput)
	}
	gotValidation, err := dir.LoadValidation()
	if err != nil || gotValidation != validation {
		t.Errorf("LoadValidation = (%q, %v), want (%q, nil)", gotValidation, err, validation)
	}
}

// TestTypedAccessors_LegacyLayoutStillLoads hand-writes files in today's flat
// naming (as if written by a pre-rundir version of the orchestrator) and
// proves the typed Load accessors still read them — the whole point of
// keeping filenames EXACTLY compatible (WP15 requirement).
func TestTypedAccessors_LegacyLayoutStillLoads(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "prompt.md"), []byte("legacy prompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "final_plan.md"), []byte("# Legacy Plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "worker_output.txt"), []byte("legacy output"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "worker_validation.txt"), []byte("legacy validation"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := Dir{Path: root}
	if got, err := dir.LoadPrompt(); err != nil || got != "legacy prompt" {
		t.Errorf("LoadPrompt on legacy layout = (%q, %v)", got, err)
	}
	if got, err := dir.LoadFinalPlan(); err != nil || got != "# Legacy Plan" {
		t.Errorf("LoadFinalPlan on legacy layout = (%q, %v)", got, err)
	}
	if got, err := dir.LoadWorkerOutput(); err != nil || got != "legacy output" {
		t.Errorf("LoadWorkerOutput on legacy layout = (%q, %v)", got, err)
	}
	if got, err := dir.LoadValidation(); err != nil || got != "legacy validation" {
		t.Errorf("LoadValidation on legacy layout = (%q, %v)", got, err)
	}
}
