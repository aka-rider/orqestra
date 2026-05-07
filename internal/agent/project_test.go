package agent

import (
	"encoding/json"
	"testing"
)

func TestProjectPlan_JSONRoundtrip(t *testing.T) {
	plan := ProjectPlan{
		SchemaVersion: "1",
		Packages: []WorkPackage{
			{
				ID:          "api-routes",
				Title:       "Implement API routes",
				Steps:       []string{"Create handler", "Add middleware"},
				Acceptance:  []string{"GET /health returns 200"},
				Constraints: []string{"No breaking changes"},
			},
			{
				ID:         "frontend",
				Title:      "Build frontend",
				Steps:      []string{"Create React app"},
				Acceptance: []string{"npm test passes"},
				DependsOn:  []string{"api-routes"},
			},
		},
	}

	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ProjectPlan
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(decoded.Packages) != 2 {
		t.Fatalf("packages: got %d, want 2", len(decoded.Packages))
	}
	if decoded.Packages[0].ID != "api-routes" {
		t.Errorf("pkg[0].ID = %q, want %q", decoded.Packages[0].ID, "api-routes")
	}
	if len(decoded.Packages[1].DependsOn) != 1 || decoded.Packages[1].DependsOn[0] != "api-routes" {
		t.Errorf("pkg[1].DependsOn = %v, want [api-routes]", decoded.Packages[1].DependsOn)
	}
}

func TestWorkPackage_ToSpecification(t *testing.T) {
	parent := Specification{
		SchemaVersion: "1",
		Goal:          "Build full-stack app",
		Context:       "Go + React monorepo",
		Scope:         &Scope{IncludeGlobs: []string{"src/**"}},
	}
	wp := WorkPackage{
		ID:          "backend",
		Title:       "Implement backend API",
		Steps:       []string{"Create server", "Add routes"},
		Acceptance:  []string{"go test passes"},
		Constraints: []string{"No external deps"},
	}

	spec := wp.ToSpecification(parent)

	if spec.Goal != "Implement backend API" {
		t.Errorf("Goal = %q, want %q", spec.Goal, "Implement backend API")
	}
	if spec.Context != parent.Context {
		t.Errorf("Context not inherited from parent")
	}
	if spec.Scope == nil {
		t.Error("Scope should be inherited from parent")
	}
	if len(spec.Steps) != 2 {
		t.Errorf("Steps: got %d, want 2", len(spec.Steps))
	}
}

func TestTopoWaves(t *testing.T) {
	packages := []WorkPackage{
		{ID: "a", Steps: []string{"do a"}},
		{ID: "b", Steps: []string{"do b"}, DependsOn: []string{"a"}},
		{ID: "c", Steps: []string{"do c"}},
		{ID: "d", Steps: []string{"do d"}, DependsOn: []string{"b", "c"}},
	}
	waves := TopoWaves(packages)

	if len(waves) < 2 {
		t.Fatalf("expected at least 2 waves, got %d", len(waves))
	}

	// Wave 0 should contain a and c (no deps)
	wave0IDs := map[string]bool{}
	for _, wp := range waves[0] {
		wave0IDs[wp.ID] = true
	}
	if !wave0IDs["a"] || !wave0IDs["c"] {
		t.Errorf("wave 0 should contain a and c, got %v", waves[0])
	}

	// d should be in a later wave than both b and c
	dWave := -1
	bWave := -1
	for i, wave := range waves {
		for _, wp := range wave {
			if wp.ID == "d" {
				dWave = i
			}
			if wp.ID == "b" {
				bWave = i
			}
		}
	}
	if dWave <= bWave {
		t.Errorf("d (wave %d) should come after b (wave %d)", dWave, bWave)
	}
}

func TestValidateProjectPlan_SelfDependency(t *testing.T) {
	plan := ProjectPlan{
		SchemaVersion: "1",
		Packages: []WorkPackage{
			{ID: "a", Title: "A", Steps: []string{"s1"}, DependsOn: []string{"a"}},
		},
	}
	err := ValidateProjectPlan(plan)
	if err == nil {
		t.Fatal("expected error for self-dependency")
	}
}

func TestValidateProjectPlan_MissingSteps(t *testing.T) {
	plan := ProjectPlan{
		SchemaVersion: "1",
		Packages: []WorkPackage{
			{ID: "a", Title: "A", Steps: nil},
		},
	}
	err := ValidateProjectPlan(plan)
	if err == nil {
		t.Fatal("expected error for package with no steps")
	}
}
