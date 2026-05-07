package agent

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

// gatewayMockRunner is a test double for harness.CLIRunner in gateway tests.
type gatewayMockRunner struct {
	output string
	err    error
}

func (m *gatewayMockRunner) RunPrint(_ context.Context, _, _ string) (harness.RunResult, error) {
	return harness.RunResult{Output: m.output}, m.err
}

func (m *gatewayMockRunner) RunStreaming(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	return harness.RunResult{Output: m.output}, m.err
}

func TestEvaluate_ValidJSON(t *testing.T) {
	runner := &gatewayMockRunner{
		output: `{"verdict":"accept","rephrased":"Build a REST API","end_state":"A running HTTP server with /health and /users endpoints, responding to GET requests with JSON. Tests pass via go test ./...","reason":"","questions":[],"improved_prompt_examples":[],"confidence":0.95}`,
	}

	gw := NewGateway(runner, &config.GatewayConfig{SystemPrompt: "test"})
	result, err := gw.Evaluate(context.Background(), "make an api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != GatewayVerdictAccept {
		t.Errorf("expected verdict accept, got %q", result.Verdict)
	}
	if result.Rephrased != "Build a REST API" {
		t.Errorf("expected rephrased 'Build a REST API', got %q", result.Rephrased)
	}
	if result.EndState == "" {
		t.Error("expected non-empty end_state for accepted result")
	}
	if result.Confidence != 0.95 {
		t.Errorf("expected confidence 0.95, got %f", result.Confidence)
	}
}

func TestEvaluate_CoachVerdict(t *testing.T) {
	runner := &gatewayMockRunner{
		output: `{"verdict":"clarify","rephrased":"Improve the code","end_state":"","reason":"No target files or behavior specified","questions":["Which module?","What metric defines better?"],"improved_prompt_examples":["Refactor internal/auth to reduce cyclomatic complexity below 10"],"confidence":0.4}`,
	}

	gw := NewGateway(runner, &config.GatewayConfig{SystemPrompt: "test"})
	result, err := gw.Evaluate(context.Background(), "make it better")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != GatewayVerdictCoach {
		t.Errorf("expected verdict clarify, got %q", result.Verdict)
	}
	if len(result.Questions) == 0 {
		t.Error("expected coaching questions")
	}
	if len(result.ImprovedPromptExamples) == 0 {
		t.Error("expected improved prompt examples")
	}
}

func TestEvaluate_RejectIsInvalidVerdict(t *testing.T) {
	runner := &gatewayMockRunner{
		output: `{"verdict":"reject","rephrased":"Rewrite the entire codebase","end_state":"","reason":"Scope too broad","questions":[],"improved_prompt_examples":[],"confidence":0.2}`,
	}

	gw := NewGateway(runner, &config.GatewayConfig{SystemPrompt: "test"})
	_, err := gw.Evaluate(context.Background(), "rewrite everything")
	if err == nil {
		t.Fatal("expected error for removed reject verdict, got nil")
	}
}

func TestEvaluate_InvalidVerdict(t *testing.T) {
	runner := &gatewayMockRunner{
		output: `{"verdict":"maybe","rephrased":"something","end_state":"","confidence":0.5}`,
	}

	gw := NewGateway(runner, &config.GatewayConfig{SystemPrompt: "test"})
	_, err := gw.Evaluate(context.Background(), "do something")
	if err == nil {
		t.Fatal("expected error for invalid verdict, got nil")
	}
}

func TestEvaluate_AcceptWithoutEndState(t *testing.T) {
	runner := &gatewayMockRunner{
		output: `{"verdict":"accept","rephrased":"Build a thing","end_state":"","confidence":0.9}`,
	}

	gw := NewGateway(runner, &config.GatewayConfig{SystemPrompt: "test"})
	_, err := gw.Evaluate(context.Background(), "build a thing")
	if err == nil {
		t.Fatal("expected error when accept verdict has empty end_state")
	}
}

func TestEvaluate_InvalidJSON(t *testing.T) {
	runner := &gatewayMockRunner{
		output: `not json at all`,
	}

	gw := NewGateway(runner, &config.GatewayConfig{SystemPrompt: "test"})
	_, err := gw.Evaluate(context.Background(), "do something")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestEvaluate_EmptyRephrased(t *testing.T) {
	runner := &gatewayMockRunner{
		output: `{"verdict":"accept","rephrased":"","end_state":"something","confidence":0.5}`,
	}

	gw := NewGateway(runner, &config.GatewayConfig{SystemPrompt: "test"})
	_, err := gw.Evaluate(context.Background(), "do something")
	if err == nil {
		t.Fatal("expected error for empty rephrased, got nil")
	}
}

func TestEvaluate_RunnerError(t *testing.T) {
	runner := &gatewayMockRunner{
		err: errors.New("cli failed"),
	}

	gw := NewGateway(runner, &config.GatewayConfig{SystemPrompt: "test"})
	_, err := gw.Evaluate(context.Background(), "do something")
	if err == nil {
		t.Fatal("expected error when runner fails, got nil")
	}
}
