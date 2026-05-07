package agent

import (
	"context"
	"errors"
	"io"
	"strings"
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

func TestGateway_AcceptClearPrompt(t *testing.T) {
	runner := &gatewayMockRunner{
		output: `{"verdict":"accept","brief":{"task":"Add token-bucket rate limiting to internal/api/gateway.go","end_state":"Rate limiter middleware in internal/api/gateway.go with tests passing","deliverables":["internal/api/gateway.go","internal/api/gateway_test.go"],"scope":["internal/api"],"non_scope":["frontend"],"acceptance_hints":["go test ./internal/api passes"]},"questions":[],"confidence":0.95,"planner_question":"How should token-bucket rate limiting be designed in internal/api/gateway.go such that it handles concurrent requests safely, given the existing middleware chain?"}`,
	}

	gw := NewGateway(runner, &config.GatewayConfig{SystemPrompt: "test"})
	result, err := gw.Evaluate(context.Background(), "Add token-bucket rate limiting to internal/api/gateway.go", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != GatewayVerdictAccept {
		t.Errorf("expected verdict accept, got %q", result.Verdict)
	}
	if result.Brief.Task == "" {
		t.Error("expected non-empty brief.task")
	}
	if result.PlannerQuestion == "" {
		t.Error("expected non-empty planner_question")
	}
}

func TestGateway_CoachVaguePrompt(t *testing.T) {
	runner := &gatewayMockRunner{
		output: `{"verdict":"coach","brief":{"task":"Improve the codebase","end_state":"","deliverables":[],"scope":[],"non_scope":[],"acceptance_hints":[]},"questions":[{"text":"Which module or package should be improved?","options":["internal/tui","internal/agent","internal/config"],"default":"internal/tui"}],"confidence":0.3,"planner_question":""}`,
	}

	gw := NewGateway(runner, &config.GatewayConfig{SystemPrompt: "test"})
	result, err := gw.Evaluate(context.Background(), "make it better", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != GatewayVerdictCoach {
		t.Errorf("expected verdict coach, got %q", result.Verdict)
	}
	if len(result.Questions) == 0 {
		t.Error("expected coaching questions")
	}
	if result.Brief.Task == "" {
		t.Error("expected brief.task to be partially populated even for coach verdict")
	}
}

func TestGateway_NeverRejects(t *testing.T) {
	runner := &gatewayMockRunner{
		output: `{"verdict":"reject","brief":{"task":"Build a SaaS platform","end_state":"","deliverables":[],"scope":[],"non_scope":[],"acceptance_hints":[]},"questions":[],"confidence":0.1,"planner_question":""}`,
	}

	gw := NewGateway(runner, &config.GatewayConfig{SystemPrompt: "test"})
	_, err := gw.Evaluate(context.Background(), "build me a SaaS platform", nil)
	if err == nil {
		t.Fatal("expected error for reject verdict (which is no longer valid), got nil")
	}
	if !strings.Contains(err.Error(), "invalid verdict") {
		t.Errorf("expected invalid verdict error, got: %v", err)
	}
}

func TestGateway_BriefAlwaysPopulated(t *testing.T) {
	runner := &gatewayMockRunner{
		output: `{"verdict":"accept","brief":{"task":"","end_state":"something","deliverables":[],"scope":[],"non_scope":[],"acceptance_hints":[]},"questions":[],"confidence":0.8,"planner_question":"How?"}`,
	}

	gw := NewGateway(runner, &config.GatewayConfig{SystemPrompt: "test"})
	_, err := gw.Evaluate(context.Background(), "do a thing", nil)
	if err == nil {
		t.Fatal("expected error when brief.task is empty")
	}
	if !strings.Contains(err.Error(), "empty brief.task") {
		t.Errorf("expected brief.task error, got: %v", err)
	}
}

func TestGateway_PlannerQuestionIsQuestion(t *testing.T) {
	runner := &gatewayMockRunner{
		output: `{"verdict":"accept","brief":{"task":"Add rate limiting","end_state":"Rate limiter works","deliverables":["api.go"],"scope":["internal/api"],"non_scope":[],"acceptance_hints":["tests pass"]},"questions":[],"confidence":0.9,"planner_question":"How should rate limiting be implemented such that it integrates with the existing middleware?"}`,
	}

	gw := NewGateway(runner, &config.GatewayConfig{SystemPrompt: "test"})
	result, err := gw.Evaluate(context.Background(), "add rate limiting", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pq := strings.ToLower(result.PlannerQuestion)
	if !strings.Contains(pq, "?") && !strings.Contains(pq, "how") && !strings.Contains(pq, "what") && !strings.Contains(pq, "which") {
		t.Errorf("planner_question should be a question, got: %q", result.PlannerQuestion)
	}
}

func TestGateway_MaxThreeQuestions(t *testing.T) {
	runner := &gatewayMockRunner{
		output: `{"verdict":"coach","brief":{"task":"Do something","end_state":"","deliverables":[],"scope":[],"non_scope":[],"acceptance_hints":[]},"questions":[{"text":"Q1","options":[],"default":""},{"text":"Q2","options":[],"default":""},{"text":"Q3","options":[],"default":""},{"text":"Q4","options":[],"default":""}],"confidence":0.2,"planner_question":""}`,
	}

	gw := NewGateway(runner, &config.GatewayConfig{SystemPrompt: "test"})
	_, err := gw.Evaluate(context.Background(), "vague input", nil)
	if err == nil {
		t.Fatal("expected error for more than 3 questions")
	}
	if !strings.Contains(err.Error(), "max is 3") {
		t.Errorf("expected max questions error, got: %v", err)
	}
}

func TestGateway_InvalidJSON(t *testing.T) {
	runner := &gatewayMockRunner{
		output: `not json at all {"broken`,
	}

	gw := NewGateway(runner, &config.GatewayConfig{SystemPrompt: "test"})
	_, err := gw.Evaluate(context.Background(), "do something", nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestGateway_StreamingParse(t *testing.T) {
	// Simulates RunStreaming returning accumulated valid JSON
	runner := &gatewayMockRunner{
		output: `{"verdict":"accept","brief":{"task":"Refactor auth module","end_state":"Auth module has reduced complexity","deliverables":["internal/auth/auth.go"],"scope":["internal/auth"],"non_scope":["internal/tui"],"acceptance_hints":["go test ./internal/auth passes"]},"questions":[],"confidence":0.88,"planner_question":"How should the auth module be refactored to reduce cyclomatic complexity below 10?"}`,
	}

	gw := NewGateway(runner, &config.GatewayConfig{SystemPrompt: "test"})
	result, err := gw.Evaluate(context.Background(), "refactor auth", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != GatewayVerdictAccept {
		t.Errorf("expected accept, got %q", result.Verdict)
	}
	if result.Brief.Task != "Refactor auth module" {
		t.Errorf("unexpected brief.task: %q", result.Brief.Task)
	}
}

func TestGateway_RunnerError(t *testing.T) {
	runner := &gatewayMockRunner{
		err: errors.New("cli failed"),
	}

	gw := NewGateway(runner, &config.GatewayConfig{SystemPrompt: "test"})
	_, err := gw.Evaluate(context.Background(), "do something", nil)
	if err == nil {
		t.Fatal("expected error when runner fails, got nil")
	}
}

func TestGateway_CoachRequiresQuestions(t *testing.T) {
	runner := &gatewayMockRunner{
		output: `{"verdict":"coach","brief":{"task":"Something vague","end_state":"","deliverables":[],"scope":[],"non_scope":[],"acceptance_hints":[]},"questions":[],"confidence":0.3,"planner_question":""}`,
	}

	gw := NewGateway(runner, &config.GatewayConfig{SystemPrompt: "test"})
	_, err := gw.Evaluate(context.Background(), "be vague", nil)
	if err == nil {
		t.Fatal("expected error when coach verdict has no questions")
	}
	if !strings.Contains(err.Error(), "at least one question") {
		t.Errorf("expected question-required error, got: %v", err)
	}
}

func TestGateway_ProseWrappedJSON(t *testing.T) {
	// Simulates a model that wraps JSON in prose (common with small local models)
	runner := &gatewayMockRunner{
		output: `I'll analyze this request for you.

{"verdict":"accept","brief":{"task":"Add E2E comment to main.go","end_state":"Comment exists on line 1","deliverables":["cmd/orqestra/main.go"],"scope":["cmd/orqestra"],"non_scope":[],"acceptance_hints":["comment present"]},"questions":[],"confidence":0.95,"planner_question":"How should a single-line comment be added to cmd/orqestra/main.go such that it appears on line 1?"}

That should cover it!`,
	}

	gw := NewGateway(runner, &config.GatewayConfig{SystemPrompt: "test"})
	result, err := gw.Evaluate(context.Background(), "add a comment", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Verdict != GatewayVerdictAccept {
		t.Errorf("expected accept, got %q", result.Verdict)
	}
	if result.Brief.Task == "" {
		t.Error("expected non-empty brief.task")
	}
}
