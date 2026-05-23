package orchestrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/testutil"
)

func testEngineWithPlanFiles(t *testing.T, researcherOutput, architectOutput, workerOutput, validationOutput string) *Engine {
	t.Helper()
	testutil.MustTempHome(t)

	researcherSID := "test-researcher-sid"
	architectSID := "test-architect-sid"

	testutil.SetupPlanFile(t, researcherSID, researcherOutput)
	testutil.SetupPlanFile(t, architectSID, architectOutput)

	workerCalls := []testutil.FakeCall{
		{Output: workerOutput, SessionID: "sess-123"},
	}
	if validationOutput != "" {
		workerCalls = append(workerCalls,
			// validation continuation
			testutil.FakeCall{Output: validationOutput, SessionID: "sess-123"},
			// commit message continuation
			testutil.FakeCall{Output: "feat: test changes", SessionID: "sess-123"},
		)
	}

	cfg := config.DefaultConfig()
	return &Engine{
		Config: cfg,
		Runners: Runners{
			Researcher: &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "saved", SessionID: researcherSID}}},
			Architect:  &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "saved", SessionID: architectSID}}},
			Worker:     &testutil.FakeRunner{Calls: workerCalls},
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
	engine := testEngineWithPlanFiles(t, "## Draft", testutil.ValidPlanMarkdown(), "done",
		agent.MarkerFail+" tests — expected 200 got 404\n"+agent.MarkerPass+" build ok")

	ctx := context.Background()
	result, err := engine.Run(ctx, Input{Prompt: "Add feature X", AutoApprove: true}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusFailed {
		t.Errorf("status = %q, want %q", result.Status, StatusFailed)
	}
}

func TestEngine_ValidationSuccessDetection(t *testing.T) {
	engine := testEngineWithPlanFiles(t, "## Draft", testutil.ValidPlanMarkdown(), "done",
		agent.MarkerPass+" tests pass\n"+agent.MarkerPass+" build ok")

	ctx := context.Background()
	result, err := engine.Run(ctx, Input{Prompt: "Add feature X", AutoApprove: true}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, StatusSuccess)
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

	criticSID := "critic-phase-sid"
	criticReport := "## Critic Report\n\n### Blockers Found\n\nNone found.\n\n### Summary\n- Total blockers: 0 (0 high, 0 medium, 0 low)\n- Overall assessment: Plan is ready for execution."
	testutil.SetupPlanFile(t, criticSID, criticReport)
	engine.Runners.Critic = &testutil.FakeRunner{
		Calls: []testutil.FakeCall{{Output: criticReport, SessionID: criticSID}},
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
			Err: fmt.Errorf("%w: used 100 of 50", ErrBudgetExhausted),
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

func TestEngine_DecisionComment_CommitsDialog(t *testing.T) {
	testutil.MustTempHome(t)

	architectSID := "test-architect-sid"
	researcherSID := "test-researcher-sid"

	testutil.SetupPlanFile(t, researcherSID, "## Draft")
	testutil.SetupPlanFile(t, architectSID, testutil.ValidPlanMarkdown())

	revisedPlan := "# Plan\n\n## Goal\nRevised goal.\n\n## Work Packages\n\n### 1. Updated\n\n**Steps:**\n1. Edit\n\n**Done when:**\n- Tests pass"

	architectRunner := &testutil.FakeRunner{
		Calls: []testutil.FakeCall{
			{Output: "saved", SessionID: architectSID},
			{Output: "revised the plan", SessionID: architectSID, OnCall: func(idx int) {
				home := os.Getenv("HOME")
				planPath := filepath.Join(home, ".claude", "plans", architectSID+"-plan.md")
				os.WriteFile(planPath, []byte(revisedPlan), 0o644)
			}},
		},
	}

	cfg := config.DefaultConfig()
	engine := &Engine{
		Config: cfg,
		Runners: Runners{
			Researcher: &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "saved", SessionID: researcherSID}}},
			Architect:  architectRunner,
			Worker:     &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "done", SessionID: "sess-w"}}},
		},
	}

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

	timeout := time.After(10 * time.Second)
	gateCount := 0
	for {
		select {
		case event, ok := <-channels.Events:
			if !ok {
				goto assertions1
			}
			if event.Type == EventGateRequest && event.Gate.Type == GatePlanApproval {
				gateCount++
				if gateCount == 1 {
					channels.Decisions <- Decision{Type: DecisionComment, Comment: "fix WP1"}
				} else {
					channels.Decisions <- Decision{Type: DecisionApprove}
				}
			}
		case <-timeout:
			t.Fatal("timeout waiting for gate cycle")
		}
	}
assertions1:
	planHistoryDir := filepath.Join(tmpDir, "run", "plan-history")
	dialogPath := filepath.Join(planHistoryDir, "dialog.md")
	dialogBytes, err := os.ReadFile(dialogPath)
	if err != nil {
		t.Fatalf("read dialog.md: %v", err)
	}
	dialog := string(dialogBytes)
	if !strings.Contains(dialog, "user") {
		t.Error("dialog.md missing user entry")
	}
	if !strings.Contains(dialog, "fix WP1") {
		t.Error("dialog.md missing comment text 'fix WP1'")
	}
	if !strings.Contains(dialog, "architect") {
		t.Error("dialog.md missing architect entry")
	}

	planPath := filepath.Join(planHistoryDir, "plan.md")
	planBytes, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan.md: %v", err)
	}
	if !strings.Contains(string(planBytes), "Revised goal") {
		t.Error("plan.md does not contain revised plan content")
	}

	out, err := exec.Command("git", "-C", planHistoryDir, "log", "--oneline").Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 3 {
		t.Errorf("expected at least 3 commits, got %d:\n%s", len(lines), string(out))
	}
}

func TestEngine_DecisionComment_ChatOnly(t *testing.T) {
	testutil.MustTempHome(t)

	architectSID := "test-architect-sid"
	researcherSID := "test-researcher-sid"

	testutil.SetupPlanFile(t, researcherSID, "## Draft")
	testutil.SetupPlanFile(t, architectSID, testutil.ValidPlanMarkdown())

	architectRunner := &testutil.FakeRunner{
		Calls: []testutil.FakeCall{
			{Output: "saved", SessionID: architectSID},
			{Output: "Because the binary wasn't rebuilt.", SessionID: architectSID},
		},
	}

	cfg := config.DefaultConfig()
	engine := &Engine{
		Config: cfg,
		Runners: Runners{
			Researcher: &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "saved", SessionID: researcherSID}}},
			Architect:  architectRunner,
			Worker:     &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "done", SessionID: "sess-w"}}},
		},
	}

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

	timeout := time.After(10 * time.Second)
	gateCount := 0
	var gotChatResponse bool
	for {
		select {
		case event, ok := <-channels.Events:
			if !ok {
				goto assertions2
			}
			if event.Type == EventChatResponse {
				gotChatResponse = true
				if event.ChatText == "" {
					t.Error("EventChatResponse has empty ChatText")
				}
			}
			if event.Type == EventGateRequest && event.Gate.Type == GatePlanApproval {
				gateCount++
				if gateCount == 1 {
					channels.Decisions <- Decision{Type: DecisionComment, Comment: "why?"}
				} else {
					channels.Decisions <- Decision{Type: DecisionApprove}
				}
			}
		case <-timeout:
			t.Fatal("timeout waiting for gate cycle")
		}
	}
assertions2:
	if !gotChatResponse {
		t.Error("expected EventChatResponse for chat-only answer")
	}

	planHistoryDir := filepath.Join(tmpDir, "run", "plan-history")
	dialogPath := filepath.Join(planHistoryDir, "dialog.md")
	dialogBytes, err := os.ReadFile(dialogPath)
	if err != nil {
		t.Fatalf("read dialog.md: %v", err)
	}
	dialog := string(dialogBytes)
	if !strings.Contains(dialog, "user") {
		t.Error("dialog.md missing user entry")
	}
	if !strings.Contains(dialog, "why?") {
		t.Error("dialog.md missing user comment 'why?'")
	}
	if !strings.Contains(dialog, "chat only") {
		t.Error("dialog.md missing '(chat only)' marker")
	}

	planPath := filepath.Join(planHistoryDir, "plan.md")
	planBytes, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan.md: %v", err)
	}
	if string(planBytes) != testutil.ValidPlanMarkdown() {
		t.Error("plan.md should remain unchanged for chat-only response")
	}
}

func TestEngine_CriticRevision_AlwaysCommitted(t *testing.T) {
	testutil.MustTempHome(t)

	architectSID := "test-architect-sid"
	researcherSID := "test-researcher-sid"

	testutil.SetupPlanFile(t, researcherSID, "## Draft")
	testutil.SetupPlanFile(t, architectSID, testutil.ValidPlanMarkdown())

	criticReport := "## Critic Report\n\n### Blockers Found\n\nNone found.\n\n### Summary\n- Total blockers: 0\n- Overall assessment: Plan is ready."
	testutil.SetupPlanFile(t, "critic-sid", criticReport)

	architectRunner := &testutil.FakeRunner{
		Calls: []testutil.FakeCall{
			{Output: "saved", SessionID: architectSID},
			{Output: "acknowledged critic, no changes needed", SessionID: architectSID},
		},
	}

	cfg := config.DefaultConfig()
	engine := &Engine{
		Config: cfg,
		Runners: Runners{
			Researcher: &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "saved", SessionID: researcherSID}}},
			Architect:  architectRunner,
			Critic:     &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: criticReport, SessionID: "critic-sid"}}},
			Worker:     &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "done", SessionID: "sess-w"}}},
		},
	}

	tmpDir := t.TempDir()
	engine.RunDirFactory = func(slug string) (agent.SessionDir, error) {
		dir := filepath.Join(tmpDir, slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return agent.SessionDir{}, err
		}
		return agent.SessionDir{Path: dir}, nil
	}

	ctx := context.Background()
	channels := engine.Start(ctx, Input{Prompt: "Add feature X", AutoApprove: true, NoExecute: true})

	for range channels.Events {
	}

	planHistoryDir := filepath.Join(tmpDir, "run", "plan-history")
	dialogPath := filepath.Join(planHistoryDir, "dialog.md")
	dialogBytes, err := os.ReadFile(dialogPath)
	if err != nil {
		t.Fatalf("read dialog.md: %v", err)
	}
	dialog := string(dialogBytes)
	if !strings.Contains(dialog, "critic") {
		t.Error("dialog.md missing critic entry")
	}
	if !strings.Contains(dialog, "no changes") {
		t.Error("dialog.md missing 'no changes' marker for architect response to critic")
	}

	out, err := exec.Command("git", "-C", planHistoryDir, "log", "--oneline").Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 3 {
		t.Errorf("expected at least 3 commits (initial plan + critic + architect response), got %d:\n%s", len(lines), string(out))
	}
}

func TestEngine_RunLog_Created(t *testing.T) {
	testutil.MustTempHome(t)

	researcherSID := "test-researcher-sid"
	architectSID := "test-architect-sid"

	testutil.SetupPlanFile(t, researcherSID, "## Draft")
	testutil.SetupPlanFile(t, architectSID, testutil.ValidPlanMarkdown())

	cfg := config.DefaultConfig()
	engine := &Engine{
		Config: cfg,
		Runners: Runners{
			Researcher: &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "saved", SessionID: researcherSID}}},
			Architect:  &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "saved", SessionID: architectSID}}},
			Worker:     &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "done", SessionID: "sess-w"}}},
		},
	}

	tmpDir := t.TempDir()
	engine.RunDirFactory = func(slug string) (agent.SessionDir, error) {
		dir := filepath.Join(tmpDir, slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return agent.SessionDir{}, err
		}
		return agent.SessionDir{Path: dir}, nil
	}

	ctx := context.Background()
	channels := engine.Start(ctx, Input{Prompt: "Add feature X", AutoApprove: true, NoExecute: true})

	for range channels.Events {
	}

	logPath := filepath.Join(tmpDir, "run", "run.log")
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("run.log should exist: %v", err)
	}
	if info.Size() == 0 {
		t.Error("run.log should not be empty")
	}
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read run.log: %v", err)
	}
	if !strings.Contains(string(content), "run started") {
		t.Error("run.log should contain 'run started'")
	}
}

func TestEngine_FullConversation_Integrity(t *testing.T) {
	testutil.MustTempHome(t)

	architectSID := "test-architect-sid"
	researcherSID := "test-researcher-sid"

	testutil.SetupPlanFile(t, researcherSID, "## Draft")
	testutil.SetupPlanFile(t, architectSID, testutil.ValidPlanMarkdown())

	criticReport := "## Critic Report\n\n### Blockers Found\n\nNone.\n\n### Summary\n- Total blockers: 0"
	testutil.SetupPlanFile(t, "critic-sid", criticReport)
	revisedPlan := "# Plan\n\n## Goal\nRevised after comment 1.\n\n## Work Packages\n\n### 1. Updated\n\n**Steps:**\n1. Edit\n\n**Done when:**\n- Tests pass"

	architectRunner := &testutil.FakeRunner{
		Calls: []testutil.FakeCall{
			// Call 0: initial plan (RunStreaming)
			{Output: "saved", SessionID: architectSID},
			// Call 1: critic continuation (no plan change)
			{Output: "acknowledged critic", SessionID: architectSID},
			// Call 2: comment 1 continuation (revises plan)
			{Output: "revised per comment 1", SessionID: architectSID, OnCall: func(idx int) {
				home := os.Getenv("HOME")
				planPath := filepath.Join(home, ".claude", "plans", architectSID+"-plan.md")
				os.WriteFile(planPath, []byte(revisedPlan), 0o644)
			}},
			// Call 3: comment 2 continuation (chat-only, no plan change)
			{Output: "That's expected behavior.", SessionID: architectSID},
		},
	}

	cfg := config.DefaultConfig()
	engine := &Engine{
		Config: cfg,
		Runners: Runners{
			Researcher: &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "saved", SessionID: researcherSID}}},
			Architect:  architectRunner,
			Critic:     &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: criticReport, SessionID: "critic-sid"}}},
			Worker:     &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "done", SessionID: "sess-w"}}},
		},
	}

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

	timeout := time.After(10 * time.Second)
	gateCount := 0
	for {
		select {
		case event, ok := <-channels.Events:
			if !ok {
				goto assertions5
			}
			if event.Type == EventGateRequest && event.Gate.Type == GatePlanApproval {
				gateCount++
				switch gateCount {
				case 1:
					channels.Decisions <- Decision{Type: DecisionComment, Comment: "please refactor WP1"}
				case 2:
					channels.Decisions <- Decision{Type: DecisionComment, Comment: "is that safe?"}
				default:
					channels.Decisions <- Decision{Type: DecisionApprove}
				}
			}
		case <-timeout:
			t.Fatal("timeout waiting for gate cycle")
		}
	}
assertions5:
	planHistoryDir := filepath.Join(tmpDir, "run", "plan-history")
	dialogPath := filepath.Join(planHistoryDir, "dialog.md")
	dialogBytes, err := os.ReadFile(dialogPath)
	if err != nil {
		t.Fatalf("read dialog.md: %v", err)
	}
	dialog := string(dialogBytes)

	// Count entries by counting "---" separators (each entry starts with one)
	entryCount := strings.Count(dialog, "---")
	if entryCount < 6 {
		t.Errorf("expected at least 6 dialog entries, got %d\n%s", entryCount, dialog)
	}

	// Verify key actors present
	if !strings.Contains(dialog, "critic") {
		t.Error("dialog.md missing critic entry")
	}
	if !strings.Contains(dialog, "user") {
		t.Error("dialog.md missing user entry")
	}
	if !strings.Contains(dialog, "architect") {
		t.Error("dialog.md missing architect entry")
	}

	// plan.md should reflect the last revision
	planPath := filepath.Join(planHistoryDir, "plan.md")
	planBytes, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan.md: %v", err)
	}
	if !strings.Contains(string(planBytes), "Revised after comment 1") {
		t.Error("plan.md should contain the revised content from comment 1")
	}

	out, err := exec.Command("git", "-C", planHistoryDir, "log", "--oneline").Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	// initial plan + critic + architect-critic + user1 + architect-revision + user2 + architect-chat-only = at least 7
	if len(lines) < 6 {
		t.Errorf("expected at least 6 commits, got %d:\n%s", len(lines), string(out))
	}
}

func TestEngine_DecisionEdit_CommitsDialog(t *testing.T) {
	testutil.MustTempHome(t)

	architectSID := "test-architect-sid"
	researcherSID := "test-researcher-sid"

	testutil.SetupPlanFile(t, researcherSID, "## Draft")
	testutil.SetupPlanFile(t, architectSID, testutil.ValidPlanMarkdown())

	cfg := config.DefaultConfig()
	engine := &Engine{
		Config: cfg,
		Runners: Runners{
			Researcher: &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "saved", SessionID: researcherSID}}},
			Architect:  &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "saved", SessionID: architectSID}}},
			Worker:     &testutil.FakeRunner{Calls: []testutil.FakeCall{{Output: "done", SessionID: "sess-w"}}},
		},
	}

	tmpDir := t.TempDir()
	engine.RunDirFactory = func(slug string) (agent.SessionDir, error) {
		dir := filepath.Join(tmpDir, slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return agent.SessionDir{}, err
		}
		return agent.SessionDir{Path: dir}, nil
	}

	editedPlan := "# Plan\n\n## Goal\nEdited by user.\n\n## Work Packages\n\n### 1. Do it\n\n**Steps:**\n1. Edit foo.go\n\n**Done when:**\n- Tests pass"

	ctx := context.Background()
	channels := engine.Start(ctx, Input{Prompt: "Add feature X"})

	timeout := time.After(10 * time.Second)
	gateCount := 0
	for {
		select {
		case event, ok := <-channels.Events:
			if !ok {
				goto assertions6
			}
			if event.Type == EventGateRequest && event.Gate.Type == GatePlanApproval {
				gateCount++
				if gateCount == 1 {
					channels.Decisions <- Decision{Type: DecisionEdit, EditedContent: editedPlan}
				} else {
					channels.Decisions <- Decision{Type: DecisionApprove}
				}
			}
		case <-timeout:
			t.Fatal("timeout waiting for gate cycle")
		}
	}
assertions6:
	planHistoryDir := filepath.Join(tmpDir, "run", "plan-history")
	dialogPath := filepath.Join(planHistoryDir, "dialog.md")
	dialogBytes, err := os.ReadFile(dialogPath)
	if err != nil {
		t.Fatalf("read dialog.md: %v", err)
	}
	dialog := string(dialogBytes)
	if !strings.Contains(dialog, "(see plan.md diff)") {
		t.Error("dialog.md should contain '(see plan.md diff)' for edit decision")
	}

	planPath := filepath.Join(planHistoryDir, "plan.md")
	planBytes, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read plan.md: %v", err)
	}
	if !strings.Contains(string(planBytes), "Edited by user") {
		t.Error("plan.md should contain the edited plan content")
	}

	out, err := exec.Command("git", "-C", planHistoryDir, "log", "--oneline").Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		t.Errorf("expected at least 2 commits (initial + edit), got %d:\n%s", len(lines), string(out))
	}
}

func TestGate_EmitsPlanHistoryDirAndHead(t *testing.T) {
	engine := testEngineWithPlanFiles(t, "## Draft", testutil.ValidPlanMarkdown(), "done", "✓ pass")

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
				t.Fatal("events closed without plan approval gate")
			}
			if event.Type == EventGateRequest && event.Gate.Type == GatePlanApproval {
				wantDir := filepath.Join(tmpDir, "run", "plan-history")
				if event.Gate.PlanHistoryDir != wantDir {
					t.Errorf("PlanHistoryDir = %q, want %q", event.Gate.PlanHistoryDir, wantDir)
				}
				if event.Gate.PlanHistoryHeadSHA == "" {
					t.Error("PlanHistoryHeadSHA should be populated when planRepo exists")
				}
				if len(event.Gate.PlanHistoryHeadSHA) < 7 {
					t.Errorf("PlanHistoryHeadSHA looks short: %q", event.Gate.PlanHistoryHeadSHA)
				}
				channels.Decisions <- Decision{Type: DecisionCancel}
				return
			}
		case <-timeout:
			t.Fatal("timeout waiting for plan gate")
		}
	}
}

func TestGate_DecisionEditEmptyComment_NoArchitect(t *testing.T) {
	engine := testEngineWithPlanFiles(t, "## Draft", testutil.ValidPlanMarkdown(), "done", "✓ pass")

	tmpDir := t.TempDir()
	engine.RunDirFactory = func(slug string) (agent.SessionDir, error) {
		dir := filepath.Join(tmpDir, slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return agent.SessionDir{}, err
		}
		return agent.SessionDir{Path: dir}, nil
	}

	editedPlan := "# Plan\n\n## Goal\nReverted.\n\n## Work Packages\n\n### 1. Do it\n\n**Steps:**\n1. Edit foo.go\n\n**Done when:**\n- Tests pass"

	ctx := context.Background()
	channels := engine.Start(ctx, Input{Prompt: "Add feature X"})

	timeout := time.After(10 * time.Second)

	var gateCount int
	architectStartsAfterEdit := 0
	editSent := false
	for {
		select {
		case event, ok := <-channels.Events:
			if !ok {
				goto assertEnd
			}
			if event.Type == EventGateRequest && event.Gate.Type == GatePlanApproval {
				gateCount++
				if gateCount == 1 {
					// Revert path: edit with empty Comment.
					channels.Decisions <- Decision{Type: DecisionEdit, EditedContent: editedPlan, Comment: ""}
					editSent = true
				} else {
					channels.Decisions <- Decision{Type: DecisionApprove}
				}
			}
			if editSent && event.Type == EventAgentStarted && event.AgentID == "architect" {
				// Only count architect starts that happen between sending the
				// empty-comment edit and the next gate re-emit.
				if gateCount == 1 {
					architectStartsAfterEdit++
				}
			}
		case <-timeout:
			t.Fatal("timeout waiting for gate cycle")
		}
	}
assertEnd:
	if architectStartsAfterEdit != 0 {
		t.Errorf("architect re-engaged on empty-comment edit (revert path broken): %d starts", architectStartsAfterEdit)
	}
	if gateCount < 2 {
		t.Errorf("expected gate to re-emit after revert, gateCount=%d", gateCount)
	}
}

func TestGate_DecisionEditAutoApprove_ProceedsToWorker(t *testing.T) {
	engine := testEngineWithPlanFiles(t, "## Draft", testutil.ValidPlanMarkdown(), "done", "✓ pass")

	tmpDir := t.TempDir()
	engine.RunDirFactory = func(slug string) (agent.SessionDir, error) {
		dir := filepath.Join(tmpDir, slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return agent.SessionDir{}, err
		}
		return agent.SessionDir{Path: dir}, nil
	}

	editedPlan := "# Plan\n\n## Goal\nUser-confirmed.\n\n## Work Packages\n\n### 1. Do it\n\n**Steps:**\n1. Edit foo.go\n\n**Done when:**\n- Tests pass"

	ctx := context.Background()
	channels := engine.Start(ctx, Input{Prompt: "Add feature X"})

	timeout := time.After(10 * time.Second)

	var gateCount int
	var workerStartedAfterEdit bool
	editSent := false
	for {
		select {
		case event, ok := <-channels.Events:
			if !ok {
				goto assertEnd
			}
			if event.Type == EventGateRequest && event.Gate.Type == GatePlanApproval {
				gateCount++
				if gateCount > 1 {
					t.Errorf("gate re-emitted after AutoApprove edit (auto-approve path broken): gateCount=%d", gateCount)
				}
				channels.Decisions <- Decision{
					Type:          DecisionEdit,
					EditedContent: editedPlan,
					AutoApprove:   true,
				}
				editSent = true
			}
			if editSent && event.Type == EventAgentStarted && event.AgentID == "worker" {
				workerStartedAfterEdit = true
			}
		case <-timeout:
			t.Fatal("timeout waiting for worker after auto-approve edit")
		}
	}
assertEnd:
	if gateCount != 1 {
		t.Errorf("expected exactly one gate request, got %d", gateCount)
	}
	if !workerStartedAfterEdit {
		t.Error("worker did not start after AutoApprove edit")
	}
}

func TestEngine_CriticStreamFallback(t *testing.T) {
	engine := testEngineWithPlanFiles(t, "## Draft", testutil.ValidPlanMarkdown(), "done", "✓ pass")

	criticSID := "critic-stream-fallback-sid"
	criticReport := "## Critic Report\n\nFallback stream report content."

	engine.Runners.Critic = &testutil.FakeRunner{
		Calls: []testutil.FakeCall{{Output: criticReport, SessionID: criticSID}},
	}

	tmpDir := t.TempDir()
	engine.RunDirFactory = func(slug string) (agent.SessionDir, error) {
		path := filepath.Join(tmpDir, ".orqestra", "sessions", "current")
		os.MkdirAll(path, 0o755)
		return agent.SessionDir{Path: path}, nil
	}

	ctx := context.Background()
	channels := engine.Start(ctx, Input{Prompt: "Add feature X", AutoApprove: true})
	for range channels.Events {
	}

	metaPath := filepath.Join(tmpDir, ".orqestra", "sessions", "current", "critic_meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read critic meta: %v", err)
	}

	if !strings.Contains(string(data), `"plan_source": "stream_fallback"`) {
		t.Errorf("critic_meta.json missing stream_fallback source: %s", string(data))
	}
}
