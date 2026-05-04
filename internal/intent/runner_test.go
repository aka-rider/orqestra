package intent

import (
	"testing"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

func TestBuildPTYCommand_NonInteractive(t *testing.T) {
	cmd := harness.BuildPTYCommand("do something", false)
	if len(cmd) != 6 {
		t.Fatalf("expected 6 args, got %d: %v", len(cmd), cmd)
	}
	if cmd[0] != "claude" {
		t.Errorf("expected claude binary, got %q", cmd[0])
	}
	if cmd[1] != "-p" {
		t.Errorf("expected -p flag, got %q", cmd[1])
	}
	if cmd[2] != "do something" {
		t.Errorf("expected prompt, got %q", cmd[2])
	}
}

func TestBuildPTYCommand_Interactive(t *testing.T) {
	cmd := harness.BuildPTYCommand("ignored", true)
	if len(cmd) != 2 {
		t.Fatalf("expected 2 args, got %d: %v", len(cmd), cmd)
	}
	if cmd[1] != "--dangerously-skip-permissions" {
		t.Errorf("expected --dangerously-skip-permissions, got %q", cmd[1])
	}
}

func TestNewIntakeRunner(t *testing.T) {
	resolved := config.ResolvedModel{
		BaseURL: "http://localhost:11434",
		Model:   "qwen3:8b",
		Type:    "openai",
	}
	small := &config.ResolvedModel{
		BaseURL: "http://localhost:11434",
		Model:   "qwen3:1b",
		Type:    "openai",
	}

	runner := NewIntakeRunner(nil, resolved, small)
	if runner == nil {
		t.Fatal("expected non-nil runner")
	}
	if runner.resolved.Model != "qwen3:8b" {
		t.Errorf("expected model qwen3:8b, got %q", runner.resolved.Model)
	}
}
