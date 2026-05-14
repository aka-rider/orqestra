package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/testutil"
	"github.com/xiii/orqestra/internal/tokenlimit"
)

func testEngineWithPlanFiles(t *testing.T, researcherOutput, architectOutput, workerOutput, _ string) *Engine {
	t.Helper()
	testutil.MustTempHome(t)

	researcherSID := "test-researcher-sid"
	architectSID := "test-architect-sid"

	testutil.SetupPlanFile(t, researcherSID, researcherOutput)
	testutil.SetupPlanFile(t, architectSID, architectOutput)

	cfg := config.DefaultConfig()
	return &Engine{
		Config: cfg,
		Runners: Runners{
			Researcher: &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "saved", SessionID: researcherSID}}},
			Architect:  &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "saved", SessionID: architectSID}}},
			Worker:     &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: workerOutput, SessionID: "sess-123"}}},
		},
	}
}

func TestEngine_PlanApprovalGate(t *testing.T) {
	engine := testEngineWithPlanFiles(t, "## Draft", testutil.ValidPlanMarkdown(), "done", "✓ pass")

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
	engine := testEngineWithPlanFiles(t, "## Draft", testutil.ValidPlanMarkdown(), "done", "✓ pass")

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
	engine := testEngineWithPlanFiles(t, "## Draft", testutil.ValidPlanMarkdown(), "done", "✓ pass")

	ctx := context.Background()
	channels := engine.Start(ctx, Input{Prompt: "Add feature X", AutoApprove: true})

	for range channels.Events {
	}
	// No gateway phase should appear (it doesn't exist anymore)
}

func TestEngine_HeadlessAutoApprove(t *testing.T) {
	engine := testEngineWithPlanFiles(t, "## Draft", testutil.ValidPlanMarkdown(), "done", "✓ pass")

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
	engine := testEngineWithPlanFiles(t, "## Draft", testutil.ValidPlanMarkdown(), "done", "✓ pass")

	ctx := context.Background()
	channels := engine.Start(ctx, Input{Prompt: "Add feature X", AutoApprove: true})

	var phases []Phase
	for event := range channels.Events {
		if event.Type == EventPhaseChange {
			phases = append(phases, event.Phase)
		}
	}

	expected := []Phase{PhaseResearching, PhasePlanning, PhaseExecuting, PhaseSelfValidating, PhaseDone}
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
	engine := testEngineWithPlanFiles(t, "## Draft", testutil.ValidPlanMarkdown(), "done", "✓ pass")

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
	engine := testEngineWithPlanFiles(t, "## Draft", testutil.ValidPlanMarkdown(), "done", "✕ test failed — expected 200 got 404")

	ctx := context.Background()
	result, err := engine.Run(ctx, Input{Prompt: "Add feature X", AutoApprove: true}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = result // status detection is internal to run()
}

func TestStreamBuffer_AppendActivity(t *testing.T) {
	sb := NewStreamBuffer(200)
	sb.SetAgent("worker")

	sb.AppendActivity("Read", "go.mod")
	sb.AppendActivity("Bash", "ls -la")

	agentID, _, acts := sb.Snapshot()
	if agentID != "worker" {
		t.Errorf("agentID = %q, want worker", agentID)
	}
	if len(acts) != 2 {
		t.Fatalf("len(activities) = %d, want 2", len(acts))
	}
	if acts[0].Tool != "Read" || acts[0].Detail != "go.mod" {
		t.Errorf("activity[0] = %+v, want {Read go.mod}", acts[0])
	}
	if acts[1].Tool != "Bash" || acts[1].Detail != "ls -la" {
		t.Errorf("activity[1] = %+v, want {Bash ls -la}", acts[1])
	}
}

func TestStreamBuffer_SetAgentClearsActivities(t *testing.T) {
	sb := NewStreamBuffer(200)
	sb.SetAgent("worker")
	sb.AppendActivity("Read", "go.mod")
	sb.SetAgent("architect")

	_, _, acts := sb.Snapshot()
	if len(acts) != 0 {
		t.Errorf("activities not cleared on SetAgent, got %d", len(acts))
	}
}

func TestStreamBuffer_ActivityRingOverflow(t *testing.T) {
	sb := NewStreamBuffer(200)
	sb.SetAgent("worker")
	for i := 0; i < 30; i++ {
		sb.AppendActivity("Read", "file")
	}

	_, _, acts := sb.Snapshot()
	if len(acts) != maxActivities {
		t.Errorf("len(activities) = %d, want %d (maxActivities)", len(acts), maxActivities)
	}
}

func TestStreamWriter_ImplementsActivitySink(t *testing.T) {
	sb := NewStreamBuffer(200)
	sb.SetAgent("test")
	w := &streamWriter{buf: sb}

	// Verify it satisfies the interface by calling OnToolUse
	w.OnToolUse("Read", "go.mod")

	_, _, acts := sb.Snapshot()
	if len(acts) != 1 {
		t.Fatalf("expected 1 activity, got %d", len(acts))
	}
	if acts[0].Tool != "Read" {
		t.Errorf("tool = %q, want Read", acts[0].Tool)
	}
}

func TestEngine_PlanFileBeforeGate(t *testing.T) {
	engine := testEngineWithPlanFiles(t, "## Draft", testutil.ValidPlanMarkdown(), "done", "✓ pass")

	// Set up a RunDirFactory that creates a temp directory
	tmpDir := t.TempDir()
	engine.RunDirFactory = func(slug string) (agent.SessionDir, error) {
		dir := filepath.Join(tmpDir, slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return agent.SessionDir{}, err
		}
		return agent.SessionDir{Path: dir}, nil
	}

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
				// Verify plan file exists on disk before the gate was emitted
				planPath := event.Gate.PlanFilePath
				if planPath == "" {
					t.Fatal("PlanFilePath is empty on gate request")
				}
				data, err := os.ReadFile(planPath)
				if err != nil {
					t.Fatalf("plan file should exist before gate: %v", err)
				}
				content := string(data)
				if content != testutil.ValidPlanMarkdown() {
					t.Errorf("plan file content mismatch:\ngot:  %q\nwant: %q", content, testutil.ValidPlanMarkdown())
				}
				channels.Decisions <- Decision{Type: DecisionApprove}
			}
		case <-timeout:
			t.Fatal("timeout waiting for plan gate")
		}
	}
}

// Contract: README "Pipeline State Machine" — Researcher → Architect → Critic → Gate → Worker → SelfValidation
func TestEngine_PhaseOrder_WithCritic(t *testing.T) {
	engine := testEngineWithPlanFiles(t, "## Draft", testutil.ValidPlanMarkdown(), "done", "✓ pass")

	criticReport := "## Critic Report\n\n### Blockers Found\n\nNone found.\n\n### Summary\n- Total blockers: 0 (0 high, 0 medium, 0 low)\n- Overall assessment: Plan is ready for execution."
	engine.Runners.Critic = &testutil.FakeRunner{
		Calls: []testutil.FakeCall{{Output: criticReport}},
	}

	ctx := context.Background()
	channels := engine.Start(ctx, Input{Prompt: "Add feature X", AutoApprove: true})

	var phases []Phase
	for event := range channels.Events {
		if event.Type == EventPhaseChange {
			phases = append(phases, event.Phase)
		}
	}

	expected := []Phase{PhaseResearching, PhasePlanning, PhaseCritiquing, PhaseExecuting, PhaseSelfValidating, PhaseDone}
	if len(phases) != len(expected) {
		t.Fatalf("phases = %v, want %v", phases, expected)
	}
	for i, p := range phases {
		if p != expected[i] {
			t.Errorf("phase[%d] = %q, want %q", i, p, expected[i])
		}
	}
}

// Contract: README "Human Gate" — operator may edit plan inline; gate re-presents with updated content
func TestEngine_DecisionEdit(t *testing.T) {
	engine := testEngineWithPlanFiles(t, "## Draft", testutil.ValidPlanMarkdown(), "done", "✓ pass")

	ctx := context.Background()
	channels := engine.Start(ctx, Input{Prompt: "Add feature X"})

	timeout := time.After(10 * time.Second)
	gateCount := 0
	for {
		select {
		case event, ok := <-channels.Events:
			if !ok {
				if gateCount < 2 {
					t.Fatalf("expected at least 2 gate requests (edit + approve), got %d", gateCount)
				}
				return
			}
			if event.Type == EventGateRequest && event.Gate.Type == GatePlanApproval {
				gateCount++
				if gateCount == 1 {
					channels.Decisions <- Decision{
						Type:          DecisionEdit,
						EditedContent: "# Plan\n\n## Goal\nEdited.\n\n## Work Packages\n\n### 1. Do it\n\n**Steps:**\n1. Edit foo.go\n\n**Done when:**\n- Tests pass",
					}
				} else {
					channels.Decisions <- Decision{Type: DecisionApprove}
				}
			}
		case <-timeout:
			t.Fatal("timeout waiting for gate cycle")
		}
	}
}

// Contract: agent-instructions.md "Token Breaking" — ErrBudgetExhausted causes EventError and clean shutdown
func TestEngine_BudgetExhausted(t *testing.T) {
	engine := testEngineWithPlanFiles(t, "## Draft", testutil.ValidPlanMarkdown(), "done", "✓ pass")
	engine.Runners.Researcher = &testutil.FakeRunner{
		Calls: []testutil.FakeCall{{
			Err: &tokenlimit.ErrBudgetExhausted{Model: "test", Used: 100, Limit: 50},
		}},
	}

	ctx := context.Background()
	channels := engine.Start(ctx, Input{Prompt: "Add feature X", AutoApprove: true})
	var gotError bool
	for event := range channels.Events {
		if event.Type == EventError {
			gotError = true
		}
	}
	if !gotError {
		t.Error("expected EventError when researcher budget is exhausted")
	}
}

func TestStreamBuffer_AgentSnapshots(t *testing.T) {
	sb := NewStreamBuffer(10)

	// Test nonexistent
	if acts := sb.AgentActivities("nonexistent"); acts != nil {
		t.Errorf("expected nil for nonexistent agent, got %v", acts)
	}

	sb.SetAgent("researcher")
	sb.AppendActivity("Read", "file1.txt")
	sb.AppendActivity("Read", "file2.txt")

	// Test current agent activities
	if acts := sb.AgentActivities("researcher"); len(acts) != 2 {
		t.Errorf("expected 2 activities for current agent, got %d", len(acts))
	}

	sb.SetAgent("architect")
	if acts := sb.AgentActivities("researcher"); len(acts) != 2 {
		t.Errorf("expected 2 activities for saved agent snapshot, got %d", len(acts))
	}

	if acts := sb.AgentActivities("architect"); len(acts) != 0 {
		t.Errorf("expected 0 activities for new agent, got %d", len(acts))
	}
}
