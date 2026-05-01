package config

import (
	"os"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Planner.Model == "" {
		t.Error("planner model should have a default")
	}
	if cfg.Validator.BaseURL == "" {
		t.Error("validator base URL should have a default")
	}
	if cfg.Retry.PlannerAttempts < 1 {
		t.Error("planner attempts should be at least 1")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	cfg, err := Load("nonexistent.yaml")
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if cfg.Planner.Model == "" {
		t.Error("should return defaults when file is missing")
	}
}

func TestLoad_EnvOverride(t *testing.T) {
	t.Setenv("ORQESTRA_VALIDATOR_URL", "http://custom:9999")
	t.Setenv("ORQESTRA_VALIDATOR_MODEL", "custom-model")

	cfg, err := Load("nonexistent.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Validator.BaseURL != "http://custom:9999" {
		t.Errorf("expected env override, got %q", cfg.Validator.BaseURL)
	}
	if cfg.Validator.Model != "custom-model" {
		t.Errorf("expected model override, got %q", cfg.Validator.Model)
	}
}

func TestLoad_ValidYAML(t *testing.T) {
	content := `
planner:
  model: test-model
validator:
  base_url: http://test:1234
  model: test-validator
`
	f, err := os.CreateTemp(t.TempDir(), "*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
	f.Close()

	cfg, err := Load(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Planner.Model != "test-model" {
		t.Errorf("expected test-model, got %q", cfg.Planner.Model)
	}
	if cfg.Validator.BaseURL != "http://test:1234" {
		t.Errorf("expected test URL, got %q", cfg.Validator.BaseURL)
	}
}
