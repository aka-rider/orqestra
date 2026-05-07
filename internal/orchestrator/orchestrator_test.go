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
	output string
	err    error
}

func (m *mockRunner) RunPrint(_ context.Context, _, _ string) (harness.RunResult, error) {
	return harness.RunResult{Output: m.output}, m.err
}

func (m *mockRunner) RunStreaming(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	return harness.RunResult{Output: m.output}, m.err
}

func acceptGatewayJSON() string {
	return `{"verdict":"accept","brief":{"task":"Add feature X","end_state":"Feature X works","deliverables":["pkg/x.go"],"scope":["pkg"],"non_scope":[],"acceptance_hints":["go test passes"]},"questions":[],"confidence":0.9,"planner_question":"How should feature X be designed?"}`
}

func coachGatewayJSON() string {
	return `{"verdict":"clarify","brief":{"task":"Improve something","end_state":"","deliverables":[],"scope":[],"non_scope":[],"acceptance_hints":[]},"questions":[{"text":"Which module?","options":["a","b"],"default":"a"}],"confidence":0.3,"planner_question":""}`
}

func validPlanJSON() string {
	return `{"goal":"Add feature X","steps":["Create pkg/x.go with X logic"],"acceptance":["go test ./pkg passes"]}`
}

func validationPassJSON() string {
	return `{"schema_version":"1","verdict":"pass","summary":"Looks good","issues":[]}`
}

func qaPassJSON() string {
	return `{"schema_version":"1","verdict":"pass","summary":"All criteria met","issues":[]}`
}

func testEngine(gatewayOutput, plannerOutput, validatorOutput, workerOutput, qaOutput string) *Engine {
	cfg := config.DefaultConfig()
	return &Engine{
		Config: cfg,
		Runners: Runners{
			Gateway:   &mockRunner{output: gatewayOutput},
			Planner:   &mockRunner{output: plannerOutput},
			Validator: &mockRunner{output: validatorOutput},
			Worker:    &mockRunner{output: workerOutput},
			QA:        &mockRunner{output: qaOutput},
		},
	}
}

func TestEngine_GatewayAcceptNoGate(t *testing.T) {
	engine := testEngine(acceptGatewayJSON(), validPlanJSON(), validationPassJSON(), "done", qaPassJSON())

	ctx := context.Background()
	channels := engine.Start(ctx, Input{Prompt: "Add feature X", AutoApprove: true})

	var gotGateRequest bool
	for event := range channels.Events {
		if event.Type == EventGateRequest {
			gotGateRequest = true
		}
	}
	if gotGateRequest {
		t.Error("expected no gate request when gateway accepts")
	}
}

func TestEngine_GatewayCoachGate(t *testing.T) {
	engine := testEngine(coachGatewayJSON(), validPlanJSON(), validationPassJSON(), "done", qaPassJSON())

	ctx := context.Background()
	channels := engine.Start(ctx, Input{Prompt: "make it better"})

	// Read events until we get a gate request
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
				// Send skip decision to unblock
				channels.Decisions <- Decision{Type: DecisionSkip}
			}
			if event.Type == EventGateRequest && event.Gate.Type == GatePlanApproval {
				// Also handle plan approval to prevent blocking
				channels.Decisions <- Decision{Type: DecisionApprove}
			}
		case <-timeout:
			t.Fatal("timeout waiting for gate request")
		}
	}
}

func TestEngine_PlanApprovalGate(t *testing.T) {
	engine := testEngine(acceptGatewayJSON(), validPlanJSON(), validationPassJSON(), "done", qaPassJSON())

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
	engine := testEngine(acceptGatewayJSON(), validPlanJSON(), validationPassJSON(), "done", qaPassJSON())

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
			if event.Type == EventComplete {
				// Pipeline completed after cancel — success
				return
			}
		case <-timeout:
			t.Fatal("timeout waiting for cancel completion")
		}
	}
}

func TestEngine_CancelMidExecution(t *testing.T) {
	// Worker that blocks until context is cancelled
	cfg := config.DefaultConfig()
	blockingRunner := &blockingMockRunner{}
	engine := &Engine{
		Config: cfg,
		Runners: Runners{
			Gateway:   &mockRunner{output: acceptGatewayJSON()},
			Planner:   &mockRunner{output: validPlanJSON()},
			Validator: &mockRunner{output: validationPassJSON()},
			Worker:    blockingRunner,
			QA:        &mockRunner{output: qaPassJSON()},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	channels := engine.Start(ctx, Input{Prompt: "Add feature X", AutoApprove: true})

	// Wait for execution phase, then cancel
	timeout := time.After(5 * time.Second)
	for {
		select {
		case event, ok := <-channels.Events:
			if !ok {
				return
			}
			if event.Type == EventPhaseChange && event.Phase == PhaseExecuting {
				cancel()
			}
			if event.Type == EventAgentFailed {
				// Worker failed due to context cancellation — expected
				return
			}
		case <-timeout:
			t.Fatal("timeout waiting for cancel mid-execution")
		}
	}
}

func TestEngine_SkipGateway(t *testing.T) {
	engine := testEngine(acceptGatewayJSON(), validPlanJSON(), validationPassJSON(), "done", qaPassJSON())

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
	engine := testEngine(acceptGatewayJSON(), validPlanJSON(), validationPassJSON(), "done", qaPassJSON())

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

func TestEngine_GatewayCoachingLoop(t *testing.T) {
	// First call returns coach, second returns accept
	callCount := 0
	cfg := config.DefaultConfig()
	switchRunner := &switchingMockRunner{
		outputs: []string{coachGatewayJSON(), acceptGatewayJSON()},
		counter: &callCount,
	}
	engine := &Engine{
		Config: cfg,
		Runners: Runners{
			Gateway:   switchRunner,
			Planner:   &mockRunner{output: validPlanJSON()},
			Validator: &mockRunner{output: validationPassJSON()},
			Worker:    &mockRunner{output: "done"},
			QA:        &mockRunner{output: qaPassJSON()},
		},
	}

	ctx := context.Background()
	channels := engine.Start(ctx, Input{Prompt: "vague prompt"})

	timeout := time.After(5 * time.Second)
	for {
		select {
		case event, ok := <-channels.Events:
			if !ok {
				if callCount < 2 {
					t.Errorf("expected at least 2 gateway calls, got %d", callCount)
				}
				return
			}
			if event.Type == EventGateRequest && event.Gate.Type == GateGatewayCoach {
				// Answer the coaching question
				channels.Decisions <- Decision{
					Type: DecisionApprove,
					GatewayAnswers: []GatewayAnswer{
						{QuestionIndex: 0, Answer: "module a"},
					},
				}
			}
			if event.Type == EventGateRequest && event.Gate.Type == GatePlanApproval {
				channels.Decisions <- Decision{Type: DecisionApprove}
			}
		case <-timeout:
			t.Fatal("timeout in coaching loop test")
		}
	}
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
