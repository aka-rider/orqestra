//go:build darwin

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/config"
)

// fakeRunner is a test double that records calls and returns configurable results.
type fakeRunner struct {
	calls    []RunConfig
	artifact []byte
	err      error
}

func (f *fakeRunner) Run(_ context.Context, cfg RunConfig) ([]byte, error) {
	f.calls = append(f.calls, cfg)
	return f.artifact, f.err
}

func TestPipeline_UsesInjectedRunner(t *testing.T) {
	repoDir := t.TempDir()
	sessDir := t.TempDir()

	fake := &fakeRunner{
		artifact: []byte(`{"status":"ok"}`),
	}

	cfg := &config.Config{}
	// Set minimal model refs so resolve doesn't fail.
	cfg.Intent.ModelRef = "test-model"
	cfg.Providers = map[string]config.ProviderConfig{
		"test": {BaseURL: "http://localhost"},
	}
	cfg.Models = map[string]config.ModelConfig{
		"test-model": {Provider: "test", Model: "gpt-test"},
	}

	pipeline := NewPipeline(PipelineConfig{
		Config:   cfg,
		RepoPath: repoDir,
		Session:  SessionDir{Path: sessDir},
	}, fake, PipelineCallbacks{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	artifact, err := pipeline.RunIntake(ctx, "test prompt")
	if err != nil {
		t.Fatalf("RunIntake: %v", err)
	}

	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 call to runner, got %d", len(fake.calls))
	}

	if fake.calls[0].Spec.Role != RoleIntake {
		t.Errorf("expected role %q, got %q", RoleIntake, fake.calls[0].Spec.Role)
	}

	if string(artifact) != `{"status":"ok"}` {
		t.Errorf("artifact = %q", artifact)
	}
}

func TestPipeline_PlannerReadonly(t *testing.T) {
	// Verify the pipeline constructs the planner with readonly access intent.
	// This test uses a real seatbelt runner to prove the planner cannot write to the repo.
	// Use a dir under HOME to avoid the base profile's temp-write allowance.
	home := os.Getenv("HOME")
	repoDir := filepath.Join(home, ".seatbelt-test-planner-readonly")
	os.MkdirAll(repoDir, 0755)
	defer os.RemoveAll(repoDir)

	sessDir := t.TempDir()

	// Place a marker file in the repo.
	os.WriteFile(filepath.Join(repoDir, "marker.txt"), []byte("original"), 0644)

	// Create a seatbelt runner configured for the planner.
	runner, err := NewSeatbeltRunner(config.SeatbeltConfig{}, nil)
	if err != nil {
		t.Fatalf("NewSeatbeltRunner: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Simulate a planner attempt to write to repo (should be denied).
	// Planner role = readonly repo.
	targetFile := filepath.Join(repoDir, "breach.txt")
	_, runErr := runner.Run(ctx, RunConfig{
		Spec: AgentSpec{
			Role:    RolePlanner,
			Command: []string{"/bin/sh", "-c", "echo BREACH > '" + targetFile + "'"},
		},
		Session:  SessionDir{Path: sessDir},
		RepoPath: repoDir,
	})

	// The command may or may not return error (depends on shell behavior),
	// but the file must NOT exist.
	_ = runErr

	if _, err := os.Stat(targetFile); err == nil {
		os.Remove(targetFile)
		t.Fatal("SECURITY FAILURE: planner wrote to repo through seatbelt runner")
	}
}

func TestPipeline_ValidatorWritesSession(t *testing.T) {
	repoDir := t.TempDir()
	sessDir := t.TempDir()

	runner, err := NewSeatbeltRunner(config.SeatbeltConfig{}, nil)
	if err != nil {
		t.Fatalf("NewSeatbeltRunner: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Validator writes its verdict to session output.
	outputDir := filepath.Join(sessDir, "plan-validator", "output")
	os.MkdirAll(outputDir, 0o755)
	outputFile := filepath.Join(outputDir, "verdict.json")

	verdict := ValidationVerdict{
		SchemaVersion: "1.0",
		Verdict:       "approved",
		Summary:       "All good",
		UserDecision:  "approved",
	}
	verdictJSON, _ := json.Marshal(verdict)

	_, runErr := runner.Run(ctx, RunConfig{
		Spec: AgentSpec{
			Role:       RolePlanValidator,
			OutputFile: "verdict.json",
			Command:    []string{"/bin/sh", "-c", "echo '" + string(verdictJSON) + "' > '" + outputFile + "'"},
		},
		Session:  SessionDir{Path: sessDir},
		RepoPath: repoDir,
	})
	if runErr != nil {
		t.Fatalf("Run failed: %v", runErr)
	}

	data, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("validator output not written to session: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("validator output is empty")
	}
}
