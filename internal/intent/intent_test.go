package intent

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
)

// mockRunner is a test double for harness.CLIRunner.
type mockRunner struct {
	output string
	err    error
}

func (m *mockRunner) RunPrint(_ context.Context, _, _ string) (harness.RunResult, error) {
	return harness.RunResult{Output: m.output}, m.err
}

func (m *mockRunner) RunStreaming(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	return harness.RunResult{Output: m.output}, m.err
}

func TestRecognize_ValidJSON(t *testing.T) {
	runner := &mockRunner{
		output: `{"rephrased": "Build a REST API", "outcome": "working API endpoint", "confidence": 0.95}`,
	}

	r := New(runner, &IntentConfig{SystemPrompt: "test"})
	intent, err := r.Recognize(context.Background(), "make an api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if intent.Rephrased != "Build a REST API" {
		t.Errorf("expected rephrased 'Build a REST API', got %q", intent.Rephrased)
	}
	if intent.Outcome != "working API endpoint" {
		t.Errorf("expected outcome 'working API endpoint', got %q", intent.Outcome)
	}
	if intent.Confidence != 0.95 {
		t.Errorf("expected confidence 0.95, got %f", intent.Confidence)
	}
}

func TestRecognize_InvalidJSON(t *testing.T) {
	runner := &mockRunner{
		output: `not json at all`,
	}

	r := New(runner, &IntentConfig{SystemPrompt: "test"})
	_, err := r.Recognize(context.Background(), "do something")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestRecognize_EmptyRephrased(t *testing.T) {
	runner := &mockRunner{
		output: `{"rephrased": "", "outcome": "something", "confidence": 0.5}`,
	}

	r := New(runner, &IntentConfig{SystemPrompt: "test"})
	_, err := r.Recognize(context.Background(), "do something")
	if err == nil {
		t.Fatal("expected error for empty rephrased, got nil")
	}
}

func TestRecognize_RunnerError(t *testing.T) {
	runner := &mockRunner{
		err: errors.New("cli failed"),
	}

	r := New(runner, &IntentConfig{SystemPrompt: "test"})
	_, err := r.Recognize(context.Background(), "do something")
	if err == nil {
		t.Fatal("expected error when runner fails, got nil")
	}
}
