package agent

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

// intentMockRunner is a test double for harness.CLIRunner in intent tests.
type intentMockRunner struct {
	output string
	err    error
}

func (m *intentMockRunner) RunPrint(_ context.Context, _, _ string) (harness.RunResult, error) {
	return harness.RunResult{Output: m.output}, m.err
}

func (m *intentMockRunner) RunStreaming(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	return harness.RunResult{Output: m.output}, m.err
}

func TestRecognize_ValidJSON(t *testing.T) {
	runner := &intentMockRunner{
		output: `{"verdict":"accept","rephrased":"Build a REST API","end_state":"A running HTTP server with /health and /users endpoints, responding to GET requests with JSON. Tests pass via go test ./...","reason":"","questions":[],"improved_prompt_examples":[],"confidence":0.95}`,
	}

	r := NewRecognizer(runner, &config.IntentConfig{SystemPrompt: "test"})
	intent, err := r.Recognize(context.Background(), "make an api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if intent.Verdict != IntentVerdictAccept {
		t.Errorf("expected verdict accept, got %q", intent.Verdict)
	}
	if intent.Rephrased != "Build a REST API" {
		t.Errorf("expected rephrased 'Build a REST API', got %q", intent.Rephrased)
	}
	if intent.EndState == "" {
		t.Error("expected non-empty end_state for accepted intent")
	}
	if intent.Confidence != 0.95 {
		t.Errorf("expected confidence 0.95, got %f", intent.Confidence)
	}
}

func TestRecognize_ClarifyVerdict(t *testing.T) {
	runner := &intentMockRunner{
		output: `{"verdict":"clarify","rephrased":"Improve the code","end_state":"","reason":"No target files or behavior specified","questions":["Which module?","What metric defines better?"],"improved_prompt_examples":["Refactor internal/auth to reduce cyclomatic complexity below 10"],"confidence":0.4}`,
	}

	r := NewRecognizer(runner, &config.IntentConfig{SystemPrompt: "test"})
	intent, err := r.Recognize(context.Background(), "make it better")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if intent.Verdict != IntentVerdictClarify {
		t.Errorf("expected verdict clarify, got %q", intent.Verdict)
	}
	if len(intent.Questions) == 0 {
		t.Error("expected clarifying questions")
	}
	if len(intent.ImprovedPromptExamples) == 0 {
		t.Error("expected improved prompt examples")
	}
}

func TestRecognize_RejectVerdict(t *testing.T) {
	runner := &intentMockRunner{
		output: `{"verdict":"reject","rephrased":"Rewrite the entire codebase","end_state":"","reason":"Scope is impossibly broad for a single execution","questions":[],"improved_prompt_examples":["Rewrite internal/auth to use OAuth2 with PKCE flow"],"confidence":0.2}`,
	}

	r := NewRecognizer(runner, &config.IntentConfig{SystemPrompt: "test"})
	intent, err := r.Recognize(context.Background(), "rewrite everything")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if intent.Verdict != IntentVerdictReject {
		t.Errorf("expected verdict reject, got %q", intent.Verdict)
	}
	if intent.Reason == "" {
		t.Error("expected non-empty reason for rejection")
	}
}

func TestRecognize_InvalidVerdict(t *testing.T) {
	runner := &intentMockRunner{
		output: `{"verdict":"maybe","rephrased":"something","end_state":"","confidence":0.5}`,
	}

	r := NewRecognizer(runner, &config.IntentConfig{SystemPrompt: "test"})
	_, err := r.Recognize(context.Background(), "do something")
	if err == nil {
		t.Fatal("expected error for invalid verdict, got nil")
	}
}

func TestRecognize_AcceptWithoutEndState(t *testing.T) {
	runner := &intentMockRunner{
		output: `{"verdict":"accept","rephrased":"Build a thing","end_state":"","confidence":0.9}`,
	}

	r := NewRecognizer(runner, &config.IntentConfig{SystemPrompt: "test"})
	_, err := r.Recognize(context.Background(), "build a thing")
	if err == nil {
		t.Fatal("expected error when accept verdict has empty end_state")
	}
}

func TestRecognize_InvalidJSON(t *testing.T) {
	runner := &intentMockRunner{
		output: `not json at all`,
	}

	r := NewRecognizer(runner, &config.IntentConfig{SystemPrompt: "test"})
	_, err := r.Recognize(context.Background(), "do something")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestRecognize_EmptyRephrased(t *testing.T) {
	runner := &intentMockRunner{
		output: `{"verdict":"accept","rephrased":"","end_state":"something","confidence":0.5}`,
	}

	r := NewRecognizer(runner, &config.IntentConfig{SystemPrompt: "test"})
	_, err := r.Recognize(context.Background(), "do something")
	if err == nil {
		t.Fatal("expected error for empty rephrased, got nil")
	}
}

func TestRecognize_RunnerError(t *testing.T) {
	runner := &intentMockRunner{
		err: errors.New("cli failed"),
	}

	r := NewRecognizer(runner, &config.IntentConfig{SystemPrompt: "test"})
	_, err := r.Recognize(context.Background(), "do something")
	if err == nil {
		t.Fatal("expected error when runner fails, got nil")
	}
}
