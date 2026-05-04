package pm

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/types"
)

type mockRunner struct {
	response string
	err      error
}

func (m *mockRunner) RunPrint(_ context.Context, _, _ string) (harness.RunResult, error) {
	if m.err != nil {
		return harness.RunResult{}, m.err
	}
	return harness.RunResult{Output: m.response}, nil
}

func (m *mockRunner) RunStreaming(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	if m.err != nil {
		return harness.RunResult{}, m.err
	}
	return harness.RunResult{Output: m.response}, nil
}

func TestDecompose_SinglePackage(t *testing.T) {
	plan := types.ProjectPlan{
		SchemaVersion: "1",
		Packages: []types.WorkPackage{
			{
				ID:         "all",
				Title:      "Implement feature",
				Steps:      []string{"Create file", "Add tests"},
				Acceptance: []string{"tests pass"},
			},
		},
	}
	data, _ := json.Marshal(plan)
	pm := New(&mockRunner{response: string(data)}, &config.ProjectManagerConfig{})

	got, err := pm.Decompose(context.Background(), types.Specification{
		Goal:  "Add feature",
		Steps: []string{"Create file", "Add tests"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Packages) != 1 {
		t.Fatalf("packages: got %d, want 1", len(got.Packages))
	}
	if got.Packages[0].ID != "all" {
		t.Errorf("package id = %q, want %q", got.Packages[0].ID, "all")
	}
}

func TestDecompose_MultiPackage(t *testing.T) {
	plan := types.ProjectPlan{
		SchemaVersion: "1",
		Packages: []types.WorkPackage{
			{
				ID:         "backend",
				Title:      "Implement API",
				Steps:      []string{"Create server", "Add routes", "Add middleware"},
				Acceptance: []string{"go test passes"},
			},
			{
				ID:         "frontend",
				Title:      "Build UI",
				Steps:      []string{"Create components", "Add styles", "Wire API"},
				Acceptance: []string{"npm test passes"},
				DependsOn:  []string{"backend"},
			},
		},
	}
	data, _ := json.Marshal(plan)
	pm := New(&mockRunner{response: string(data)}, &config.ProjectManagerConfig{})

	got, err := pm.Decompose(context.Background(), types.Specification{
		Goal:  "Full-stack app",
		Steps: []string{"Create server", "Add routes", "Add middleware", "Create components", "Add styles", "Wire API"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Packages) != 2 {
		t.Fatalf("packages: got %d, want 2", len(got.Packages))
	}
}

func TestDecompose_EmptyPackages(t *testing.T) {
	plan := types.ProjectPlan{SchemaVersion: "1", Packages: nil}
	data, _ := json.Marshal(plan)
	pm := New(&mockRunner{response: string(data)}, &config.ProjectManagerConfig{})

	_, err := pm.Decompose(context.Background(), types.Specification{
		Goal:  "Do thing",
		Steps: []string{"step"},
	})
	if err == nil {
		t.Fatal("expected error for empty packages")
	}
}

func TestDecompose_InvalidCycle(t *testing.T) {
	plan := types.ProjectPlan{
		SchemaVersion: "1",
		Packages: []types.WorkPackage{
			{ID: "a", Title: "A", Steps: []string{"s1"}, DependsOn: []string{"b"}},
			{ID: "b", Title: "B", Steps: []string{"s2"}, DependsOn: []string{"a"}},
		},
	}
	data, _ := json.Marshal(plan)
	pm := New(&mockRunner{response: string(data)}, &config.ProjectManagerConfig{})

	_, err := pm.Decompose(context.Background(), types.Specification{
		Goal:  "Do thing",
		Steps: []string{"s1", "s2"},
	})
	if err == nil {
		t.Fatal("expected error for cyclic dependencies")
	}
}

func TestDecompose_MissingDependency(t *testing.T) {
	plan := types.ProjectPlan{
		SchemaVersion: "1",
		Packages: []types.WorkPackage{
			{ID: "a", Title: "A", Steps: []string{"s1"}, DependsOn: []string{"nonexistent"}},
		},
	}
	data, _ := json.Marshal(plan)
	pm := New(&mockRunner{response: string(data)}, &config.ProjectManagerConfig{})

	_, err := pm.Decompose(context.Background(), types.Specification{
		Goal:  "Do thing",
		Steps: []string{"s1"},
	})
	if err == nil {
		t.Fatal("expected error for missing dependency")
	}
}

func TestDecompose_DuplicateID(t *testing.T) {
	plan := types.ProjectPlan{
		SchemaVersion: "1",
		Packages: []types.WorkPackage{
			{ID: "a", Title: "A", Steps: []string{"s1"}},
			{ID: "a", Title: "B", Steps: []string{"s2"}},
		},
	}
	data, _ := json.Marshal(plan)
	pm := New(&mockRunner{response: string(data)}, &config.ProjectManagerConfig{})

	_, err := pm.Decompose(context.Background(), types.Specification{
		Goal:  "Do thing",
		Steps: []string{"s1", "s2"},
	})
	if err == nil {
		t.Fatal("expected error for duplicate package ID")
	}
}

func TestDecompose_CodeFencedOutput(t *testing.T) {
	plan := types.ProjectPlan{
		SchemaVersion: "1",
		Packages: []types.WorkPackage{
			{ID: "core", Title: "Core", Steps: []string{"s1"}, Acceptance: []string{"ok"}},
		},
	}
	data, _ := json.Marshal(plan)
	wrapped := "```json\n" + string(data) + "\n```"
	pm := New(&mockRunner{response: wrapped}, &config.ProjectManagerConfig{})

	got, err := pm.Decompose(context.Background(), types.Specification{
		Goal:  "Do thing",
		Steps: []string{"s1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Packages) != 1 {
		t.Fatalf("packages: got %d, want 1", len(got.Packages))
	}
}

func TestValidatePlan_SelfDependency(t *testing.T) {
	plan := types.ProjectPlan{
		SchemaVersion: "1",
		Packages: []types.WorkPackage{
			{ID: "a", Title: "A", Steps: []string{"s1"}, DependsOn: []string{"a"}},
		},
	}
	err := validatePlan(plan)
	if err == nil {
		t.Fatal("expected error for self-dependency")
	}
}

func TestValidatePlan_MissingSteps(t *testing.T) {
	plan := types.ProjectPlan{
		SchemaVersion: "1",
		Packages: []types.WorkPackage{
			{ID: "a", Title: "A", Steps: nil},
		},
	}
	err := validatePlan(plan)
	if err == nil {
		t.Fatal("expected error for package with no steps")
	}
}
