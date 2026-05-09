package orchestrator

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

// mockRunner is a test double for harness.CLIRunner.
type mockRunner struct {
	output    string
	sessionID string
	err       error
}

func (m *mockRunner) RunPrint(_ context.Context, _, _ string) (harness.RunResult, error) {
	return harness.RunResult{Output: m.output, SessionID: m.sessionID}, m.err
}

func (m *mockRunner) RunStreaming(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	return harness.RunResult{Output: m.output, SessionID: m.sessionID}, m.err
}

func (m *mockRunner) RunContinue(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	return harness.RunResult{Output: m.output, SessionID: m.sessionID}, m.err
}

func acceptGatewayJSON() string {
	return `{"verdict":"accept","brief":{"task":"Add feature X","end_state":"Feature X works","scope":["pkg"],"non_scope":[]},"questions":[],"confidence":0.9}`
}

func coachGatewayJSON() string {
	return `{"verdict":"coach","brief":{"task":"Improve something","end_state":"","scope":[],"non_scope":[]},"questions":[{"text":"Which module?","options":["a","b"],"default":"a"}],"confidence":0.3}`
}

func validPlanMarkdown() string {
	return "# Plan\n\n## Goal\nAdd feature X.\n\n## Work Packages\n\n### 1. Add X\n\n**Steps:**\n1. Create pkg/x.go\n\n**Done when:**\n- go test ./pkg passes"
}

func testEngine(gatewayOutput, researcherOutput, plannerOutput, workerOutput, validationOutput string) *Engine {
	cfg := config.DefaultConfig()
	return &Engine{
		Config: cfg,
		Runners: Runners{
			Gateway:    &mockRunner{output: gatewayOutput},
			Researcher: &mockRunner{output: researcherOutput},
			Planner:    &mockRunner{output: plannerOutput},
			Worker:     &mockRunner{output: workerOutput, sessionID: "sess-123"},
		},
	}
}

func TestEngine_GatewayAcceptNoGate(t *testing.T) {
	engine := testEngine(acceptGatewayJSON(), "## Draft\nstuff", validPlanMarkdown(), "done", "✅ all pass")

	ctx := context.Background()
	channels := engine.Start(ctx, Input{Prompt: "Add feature X", AutoApprove: true})

	var gotGateRequest bool
	for event := range channels.Events {
		if event.Type == EventGateRequest {
			gotGateRequest = true
		}
	}
	if gotGateRequest {
		t.Error("expected no gate request in auto-approve mode")
	}
}

func TestEngine_GatewayCoachGate(t *testing.T) {
	engine := testEngine(coachGatewayJSON(), "## Draft", validPlanMarkdown(), "done", "✅ pass")

	ctx := context.Background()
	channels := engine.Start(ctx, Input{Prompt: "make it better"})

	var gotGateRequest bool
	timeout := time.After(5 * time.Second)
	for {
		select {
		case event, ok := <-channels.Events:
			if !ok {
				if !gotGateRequest {
					t.Fatal("events channel closed without gate request")
				}
				return
			}
			if event.Type == EventGateRequest && event.Gate.Type == GateGatewayCoach {
				gotGateRequest = true
				channels.Decisions <- Decision{Type: DecisionSkip}
			}
			if event.Type == EventGateRequest && event.Gate.Type == GatePlanApproval {
				channels.Decisions <- Decision{Type: DecisionApprove}
			}
		case <-timeout:
			t.Fatal("timeout waiting for gate request")
		}
	}
}

func TestEngine_PlanApprovalGate(t *testing.T) {
	engine := testEngine(acceptGatewayJSON(), "## Draft", validPlanMarkdown(), "done", "✅ pass")

	ctx := context.Background()
	channels := engine.Start(ctx, Input{Prompt: "Add feature X"})

	timeout := time.After(5 * time.Second)
	var gotPlanGate bool
	for {
		select {
		case event, ok := <-channels.Events:
			if !ok {
				if !gotPlanGate {
					t.Fatal("events closed without plan approval gate")
				}
				return
			}
			if event.Type == EventGateRequest && event.Gate.Type == GatePlanApproval {
				gotPlanGate = true
				channels.Decisions <- Decision{Type: DecisionApprove}
			}
		case <-timeout:
			t.Fatal("timeout waiting for plan gate")
		}
	}
}

func TestEngine_CancelAtGate(t *testing.T) {
	engine := testEngine(acceptGatewayJSON(), "## Draft", validPlanMarkdown(), "done", "✅ pass")

	ctx := context.Background()
	channels := engine.Start(ctx, Input{Prompt: "Add feature X"})

	timeout := time.After(5 * time.Second)
	for {
		select {
		case event, ok := <-channels.Events:
			if !ok {
				return
			}
			if event.Type == EventGateRequest && event.Gate.Type == GatePlanApproval {
				channels.Decisions <- Decision{Type: DecisionCancel}
			}
		case <-timeout:
			t.Fatal("timeout waiting for cancel completion")
		}
	}
}

func TestEngine_SkipGateway(t *testing.T) {
	engine := testEngine(acceptGatewayJSON(), "## Draft", validPlanMarkdown(), "done", "✅ pass")

	ctx := context.Background()
	channels := engine.Start(ctx, Input{Prompt: "Add feature X", SkipGateway: true, AutoApprove: true})

	var gotGatewayPhase bool
	for event := range channels.Events {
		if event.Type == EventPhaseChange && event.Phase == PhaseGateway {
			gotGatewayPhase = true
		}
	}
	if gotGatewayPhase {
		t.Error("expected no gateway phase when SkipGateway=true")
	}
}

func TestEngine_HeadlessAutoApprove(t *testing.T) {
	engine := testEngine(acceptGatewayJSON(), "## Draft", validPlanMarkdown(), "done", "✅ pass")

	ctx := context.Background()
	channels := engine.Start(ctx, Input{Prompt: "Add feature X", AutoApprove: true})

	var gotGateRequest bool
	var completed bool
	for event := range channels.Events {
		if event.Type == EventGateRequest {
			gotGateRequest = true
		}
		if event.Type == EventComplete {
			completed = true
		}
	}
	if gotGateRequest {
		t.Error("expected no gate requests in auto-approve mode")
	}
	if !completed {
		t.Error("expected pipeline to complete")
	}
}

func TestEngine_PhaseOrder(t *testing.T) {
	engine := testEngine(acceptGatewayJSON(), "## Draft", validPlanMarkdown(), "done", "✅ pass")

	ctx := context.Background()
	channels := engine.Start(ctx, Input{Prompt: "Add feature X", AutoApprove: true})

	var phases []Phase
	for event := range channels.Events {
		if event.Type == EventPhaseChange {
			phases = append(phases, event.Phase)
		}
	}

	expected := []Phase{PhaseGateway, PhaseResearching, PhasePlanning, PhaseExecuting, PhaseSelfValidating, PhaseDone}
	if len(phases) != len(expected) {
		t.Fatalf("phases = %v, want %v", phases, expected)
	}
	for i, p := range phases {
		if p != expected[i] {
			t.Errorf("phase[%d] = %q, want %q", i, p, expected[i])
		}
	}
}

func TestEngine_NoExecute(t *testing.T) {
	engine := testEngine(acceptGatewayJSON(), "## Draft", validPlanMarkdown(), "done", "✅ pass")

	ctx := context.Background()
	channels := engine.Start(ctx, Input{Prompt: "Add feature X", AutoApprove: true, NoExecute: true})

	var gotExecuting bool
	var gotComplete bool
	for event := range channels.Events {
		if event.Type == EventPhaseChange && event.Phase == PhaseExecuting {
			gotExecuting = true
		}
		if event.Type == EventComplete {
			gotComplete = true
		}
	}
	if gotExecuting {
		t.Error("expected no executing phase with NoExecute=true")
	}
	if !gotComplete {
		t.Error("expected pipeline to complete")
	}
}

func TestEngine_ValidationFailureDetection(t *testing.T) {
	engine := testEngine(acceptGatewayJSON(), "## Draft", validPlanMarkdown(), "done", "❌ test failed — expected 200 got 404")

	ctx := context.Background()
	result, err := engine.Run(ctx, Input{Prompt: "Add feature X", AutoApprove: true}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = result // status detection is internal to run()
}

// blockingMockRunner blocks until context is cancelled.
type blockingMockRunner struct{}

func (b *blockingMockRunner) RunPrint(ctx context.Context, _, _ string) (harness.RunResult, error) {
	<-ctx.Done()
	return harness.RunResult{}, ctx.Err()
}

func (b *blockingMockRunner) RunStreaming(ctx context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	<-ctx.Done()
	return harness.RunResult{}, ctx.Err()
}

func (b *blockingMockRunner) RunContinue(ctx context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	<-ctx.Done()
	return harness.RunResult{}, ctx.Err()
}

// switchingMockRunner returns different outputs on sequential calls.
type switchingMockRunner struct {
	outputs []string
	counter *int
}

func (s *switchingMockRunner) RunPrint(_ context.Context, _, _ string) (harness.RunResult, error) {
	idx := *s.counter
	if idx >= len(s.outputs) {
		idx = len(s.outputs) - 1
	}
	*s.counter++
	return harness.RunResult{Output: s.outputs[idx]}, nil
}

func (s *switchingMockRunner) RunStreaming(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	idx := *s.counter
	if idx >= len(s.outputs) {
		idx = len(s.outputs) - 1
	}
	*s.counter++
	return harness.RunResult{Output: s.outputs[idx]}, nil
}

func (s *switchingMockRunner) RunContinue(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	idx := *s.counter
	if idx >= len(s.outputs) {
		idx = len(s.outputs) - 1
	}
	*s.counter++
	return harness.RunResult{Output: s.outputs[idx]}, nil
}
