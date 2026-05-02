package plan

import (
	"os"
	"path/filepath"
	"testing"
)

func fullSpec() Spec {
	return Spec{
		SchemaVersion:      "1",
		Goal:               "Add tests for the user service",
		Context:            "The user service is tested using Go's testing package.",
		Steps:              []string{"Create user_service_test.go", "Write unit tests for CreateUser", "Write unit tests for GetUser"},
		Acceptance:         []string{"All tests pass with go test ./...", "Coverage above 80%"},
		Constraints:        []string{"Do not modify production code"},
		Risks:              []string{"Tests may be flaky if database is not mocked"},
		ValidationCommands: []string{"go test ./...", "go vet ./..."},
		ExpectedArtifacts:  []string{"internal/user/user_service_test.go"},
	}
}

// TestRoundTrip verifies MarshalMarkdown → UnmarshalMarkdown preserves all nine fields.
func TestRoundTrip(t *testing.T) {
	original := fullSpec()
	data, err := MarshalMarkdown(original)
	if err != nil {
		t.Fatalf("MarshalMarkdown: %v", err)
	}
	got, err := UnmarshalMarkdown(data)
	if err != nil {
		t.Fatalf("UnmarshalMarkdown: %v", err)
	}

	assertEqual(t, "SchemaVersion", original.SchemaVersion, got.SchemaVersion)
	assertEqual(t, "Goal", original.Goal, got.Goal)
	assertEqual(t, "Context", original.Context, got.Context)
	assertSliceEqual(t, "Steps", original.Steps, got.Steps)
	assertSliceEqual(t, "Acceptance", original.Acceptance, got.Acceptance)
	assertSliceEqual(t, "Constraints", original.Constraints, got.Constraints)
	assertSliceEqual(t, "Risks", original.Risks, got.Risks)
	assertSliceEqual(t, "ValidationCommands", original.ValidationCommands, got.ValidationCommands)
	assertSliceEqual(t, "ExpectedArtifacts", original.ExpectedArtifacts, got.ExpectedArtifacts)
}

// TestGoldenFileParse verifies parsing of a hand-authored markdown fixture.
func TestGoldenFileParse(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "golden.md"))
	if err != nil {
		t.Fatalf("reading golden file: %v", err)
	}
	got, err := UnmarshalMarkdown(data)
	if err != nil {
		t.Fatalf("UnmarshalMarkdown golden: %v", err)
	}

	want := fullSpec()
	assertEqual(t, "SchemaVersion", want.SchemaVersion, got.SchemaVersion)
	assertEqual(t, "Goal", want.Goal, got.Goal)
	assertEqual(t, "Context", want.Context, got.Context)
	assertSliceEqual(t, "Steps", want.Steps, got.Steps)
	assertSliceEqual(t, "Acceptance", want.Acceptance, got.Acceptance)
	assertSliceEqual(t, "Constraints", want.Constraints, got.Constraints)
	assertSliceEqual(t, "Risks", want.Risks, got.Risks)
	assertSliceEqual(t, "ValidationCommands", want.ValidationCommands, got.ValidationCommands)
	assertSliceEqual(t, "ExpectedArtifacts", want.ExpectedArtifacts, got.ExpectedArtifacts)
}

// TestSaveAndLoadFile verifies SaveToFile / LoadFromFile integration.
func TestSaveAndLoadFile(t *testing.T) {
	dir := t.TempDir()
	original := fullSpec()

	path, err := SaveToFile(original, dir)
	if err != nil {
		t.Fatalf("SaveToFile: %v", err)
	}
	if path == "" {
		t.Fatal("SaveToFile returned empty path")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("file not created at %s: %v", path, statErr)
	}

	loaded, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}

	assertEqual(t, "Goal", original.Goal, loaded.Goal)
	assertSliceEqual(t, "Steps", original.Steps, loaded.Steps)
	assertSliceEqual(t, "ValidationCommands", original.ValidationCommands, loaded.ValidationCommands)
}

// TestLoadFromFile_MissingFile verifies a hard error on a non-existent path.
func TestLoadFromFile_MissingFile(t *testing.T) {
	_, err := LoadFromFile("/nonexistent/path/plan.md")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

// TestUnmarshalMarkdown_MissingGoal verifies rejection of documents without Goal.
func TestUnmarshalMarkdown_MissingGoal(t *testing.T) {
	md := "# Orqestra Plan\n\n## SchemaVersion\n\n1\n\n## Steps\n\n1. step one\n"
	_, err := UnmarshalMarkdown([]byte(md))
	if err == nil {
		t.Fatal("expected error for missing Goal, got nil")
	}
}

// TestUnmarshalMarkdown_MissingSchemaVersion verifies rejection of documents without SchemaVersion.
func TestUnmarshalMarkdown_MissingSchemaVersion(t *testing.T) {
	md := "# Orqestra Plan\n\n## Goal\n\nDo something\n"
	_, err := UnmarshalMarkdown([]byte(md))
	if err == nil {
		t.Fatal("expected error for missing SchemaVersion, got nil")
	}
}

func assertEqual(t *testing.T, field, want, got string) {
	t.Helper()
	if want != got {
		t.Errorf("%s: want %q, got %q", field, want, got)
	}
}

func assertSliceEqual(t *testing.T, field string, want, got []string) {
	t.Helper()
	if len(want) != len(got) {
		t.Errorf("%s: want len %d, got len %d (want %v, got %v)", field, len(want), len(got), want, got)
		return
	}
	for i := range want {
		if want[i] != got[i] {
			t.Errorf("%s[%d]: want %q, got %q", field, i, want[i], got[i])
		}
	}
}
