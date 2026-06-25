package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/mcp"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// testModel creates a Model suitable for testing with a minimal mock engine.
func testModel() Model {
	engine := &orchestrator.Engine{
		Config: testConfig(),
	}
	m, err := NewModel(engine, "test.yaml")
	if err != nil {
		panic("testModel: NewModel: " + err.Error())
	}
	m.width = 120
	m.height = 40
	m.recalculateLayout()
	return m
}

func testConfig() *config.Config {
	return &config.Config{
		Researcher: config.ResearcherConfig{},
		Architect:  config.ArchitectConfig{},
		Worker:     config.WorkerConfig{},
	}
}

func sendKey(m tea.Model, key rune) (tea.Model, tea.Cmd) {
	return m.Update(tea.KeyPressMsg{Code: key})
}

func sendRune(m tea.Model, r string) (tea.Model, tea.Cmd) {
	return m.Update(tea.KeyPressMsg{Code: rune(r[0]), Text: r})
}

func sendCtrl(m tea.Model, key rune) (tea.Model, tea.Cmd) {
	return m.Update(tea.KeyPressMsg{Code: key, Mod: tea.ModCtrl})
}

func sendCtrlShift(m tea.Model, key rune) (tea.Model, tea.Cmd) {
	return m.Update(tea.KeyPressMsg{Code: key, Mod: tea.ModCtrl | tea.ModShift})
}

func sendAlt(m tea.Model, key rune) (tea.Model, tea.Cmd) {
	return m.Update(tea.KeyPressMsg{Code: key, Mod: tea.ModAlt})
}

// viewString extracts the rendered content string from the tea.View returned by Model.View().
func viewString(m Model) string {
	return m.View().Content
}

func TestTUI_PromptSubmit(t *testing.T) {
	m := testModel()
	// Set prompt value directly (textarea handles rune input internally)
	m.promptScreen.SetValue("add a feature")

	// Press Enter to submit — intent arrives on next Update cycle
	result, cmd := sendKey(m, tea.KeyEnter)
	model := result.(Model)

	if model.state != StatePipeline {
		t.Errorf("expected StatePipeline, got %d", model.state)
	}
	if model.pipelineScreen.content != ContentStreaming {
		t.Errorf("expected ContentStreaming, got %d", model.pipelineScreen.content)
	}
	if model.pipelineScreen.goal != "add a feature" {
		t.Errorf("expected goal 'add a feature', got %q", model.pipelineScreen.goal)
	}
	// ObsStore + Control must be set on the returned model (regression: evaluation-order bug).
	if model.obs == nil {
		t.Error("model.obs is nil after prompt submit — pipeline state will never be received")
	}
	if model.pipelineScreen.streamBuf == nil {
		t.Error("model.pipelineScreen.streamBuf is nil after prompt submit — streaming output will not display")
	}
	if model.ctrl == nil {
		t.Error("model.ctrl is nil after prompt submit — gate responses will never be sent")
	}
	if model.cancel == nil {
		t.Error("model.cancel is nil after prompt submit — pipeline cannot be stopped")
	}
	// Cmd should be non-nil (waitForEvent + tick)
	if cmd == nil {
		t.Error("expected non-nil cmd from startPipeline")
	}
	// Clean up: cancel the pipeline
	model.cancel()
}

func TestTUI_PromptEmptyIgnored(t *testing.T) {
	m := testModel()
	// Empty prompt — Enter should be ignored
	result, _ := sendKey(m, tea.KeyEnter)
	model := result.(Model)

	if model.state != StatePrompt {
		t.Error("expected to stay in StatePrompt with empty prompt")
	}
}

func TestTUI_PlanApproval(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming

	snap := orchestrator.ObsSnapshot{
		HasGate: true,
		Gate: orchestrator.GateRequest{
			Position:          orchestrator.GateAfterDeliberation,
			FinalPlanMarkdown: "# Plan\n\n## Goal\nAdd feature X\n\n## Work Packages\n\n### 1. Step 1",
		},
	}
	m.pipelineScreen.ApplySnapshot(snap, m.width)

	if m.pipelineScreen.content != ContentHumanGate {
		t.Errorf("expected ContentHumanGate, got %d", m.pipelineScreen.content)
	}
	if !m.pipelineScreen.hasPlan {
		t.Error("expected hasPlan=true")
	}
}

func TestTUI_PlanApprove(t *testing.T) {
	t.Skip("skipped: PlanApprove flow replaced by HumanChatMode in v6")
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentHumanGate
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nTest"
	// channel removed — test checks state only

	result, _ := sendCtrl(m, 'a')
	model := result.(Model)

	if model.pipelineScreen.content != ContentStreaming {
		t.Errorf("expected ContentStreaming after approve, got %d", model.pipelineScreen.content)
	}
}

// ^E on the plan gate writes the plan to a temp file and asks the app to open
// $EDITOR; the gate stays open while editing happens out-of-band (D8).
func TestTUI_PlanEditOpensExternalEditor(t *testing.T) {
	const plan = "# Plan\n\n## Goal\nOriginal"
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentHumanGate
	m.pipelineScreen.activeChat = newHumanChatMode(orchestrator.GateRequest{Position: orchestrator.GateAfterDeliberation}, m.keys)
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = plan

	result, _ := sendCtrl(m, 'e')
	model := result.(Model)

	if model.pipelineScreen.content != ContentHumanGate {
		t.Errorf("expected ContentHumanGate (unchanged), got %d", model.pipelineScreen.content)
	}
	path := model.pipelineScreen.editorFilePath
	if path == "" {
		t.Fatal("expected editorFilePath to be set after ^E")
	}
	defer os.Remove(path)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read temp plan: %v", err)
	}
	if string(got) != plan {
		t.Errorf("temp plan = %q, want %q", got, plan)
	}
}

func TestTUI_CancelAgent(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.pipelineScreen.active = true
	cancelled := false
	m.cancel = func() { cancelled = true }

	// First Ctrl+C cancels the pipeline
	result, _ := sendCtrl(m, 'c')
	model := result.(Model)

	if !cancelled {
		t.Error("expected cancel func to be called")
	}
	if !model.ctrlCPending {
		t.Error("expected ctrlCPending=true after first Ctrl+C")
	}
}

func TestTUI_UserQuestion_CtrlCSkipsWithDefault(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.active = true
	m.pipelineScreen.chat.OpenQuestion(mcp.ToolCall{
		Question: "Pick one",
		Options:  []mcp.ToolOption{{Label: "Yes"}, {Label: "No"}},
	}, 80)

	updated, _ := sendCtrl(m, 'c')
	mm := updated.(Model)

	if mm.pipelineScreen.content != ContentStreaming {
		t.Fatalf("expected ContentStreaming after Ctrl+C, got %v", mm.pipelineScreen.content)
	}
	if !mm.ctrlCPending {
		t.Fatalf("expected ctrlCPending after first Ctrl+C")
	}
	// testModel() does not wire QuestionBridge, so processIntent silently
	// drops the SubmitQuestionAnswerIntent (model.go: bridge==nil branch).
	// The bridge-receives-Skipped:true assertion lives in the direct
	// HandleCtrlCCancel test in screen_pipeline_test.go.
}

func TestTUI_NewRun(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentCompletion
	m.pipelineScreen.goal = "original goal"

	result, _ := sendCtrl(m, 'n')
	model := result.(Model)

	if model.state != StatePrompt {
		t.Errorf("expected StatePrompt, got %d", model.state)
	}
	if !strings.Contains(model.promptScreen.Value(), "original goal") {
		t.Errorf("expected prompt pre-filled with goal, got %q", model.promptScreen.Value())
	}
}

func TestTUI_NewRunConfirm(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.pipelineScreen.goal = "active task"
	m.pipelineScreen.active = true
	cancelled := false
	m.cancel = func() { cancelled = true }

	// Press Ctrl+N during active pipeline — directly confirms new run
	result, _ := sendCtrl(m, 'n')
	model := result.(Model)

	if model.state != StatePrompt {
		t.Errorf("expected StatePrompt after Ctrl+N, got %d", model.state)
	}
	if !cancelled {
		t.Error("expected cancel to be called")
	}
	if !strings.Contains(model.promptScreen.Value(), "active task") {
		t.Errorf("expected prompt pre-filled with goal, got %q", model.promptScreen.Value())
	}
}

func TestTUI_SidebarUpdates(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming

	// Agent appears in snapshot as running.
	m.pipelineScreen.ApplySnapshot(orchestrator.ObsSnapshot{
		Agents: []orchestrator.AgentSnapshot{
			{AgentID: "researcher", Status: "running"},
		},
	}, m.width)

	if len(m.pipelineScreen.agents) != 1 || m.pipelineScreen.agents[0].State != AgentStateRunning {
		t.Errorf("expected 1 agent running, got %+v", m.pipelineScreen.agents)
	}

	// Agent transitions to done.
	m.pipelineScreen.ApplySnapshot(orchestrator.ObsSnapshot{
		Agents: []orchestrator.AgentSnapshot{
			{AgentID: "researcher", Status: "done"},
		},
	}, m.width)

	if m.pipelineScreen.agents[0].State != AgentStateDone {
		t.Errorf("expected agent state 'done', got %q", m.pipelineScreen.agents[0].State)
	}
}

func TestTUI_DoubleCtrlC(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.pipelineScreen.active = true
	m.cancel = func() {}

	// First Ctrl+C — cancels pipeline, sets pending
	result, cmd := sendCtrl(m, 'c')
	model := result.(Model)
	if cmd == nil {
		t.Error("first Ctrl+C should return timeout tick cmd")
	}
	if !model.ctrlCPending {
		t.Error("expected ctrlCPending=true")
	}

	// Second Ctrl+C within time gate — should quit
	_, cmd = sendCtrl(model, 'c')
	if cmd == nil {
		t.Error("second Ctrl+C should trigger quit")
	}
}

func TestTUI_CtrlCQuitWhenIdle(t *testing.T) {
	m := testModel()

	// Ctrl+C from prompt screen (idle) — immediate quit
	_, cmd := sendCtrl(m, 'c')
	if cmd == nil {
		t.Error("Ctrl+C when idle should trigger immediate quit")
	}
}

func TestTUI_CtrlCTimeoutResets(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.pipelineScreen.active = true
	m.cancel = func() {}

	// First Ctrl+C
	result, _ := sendCtrl(m, 'c')
	model := result.(Model)
	if !model.ctrlCPending {
		t.Fatal("expected ctrlCPending=true")
	}

	// Simulate timeout message
	result2, _ := model.Update(ctrlCTimeoutMsg{})
	model2 := result2.(Model)
	if model2.ctrlCPending {
		t.Error("expected ctrlCPending=false after timeout")
	}
}

func TestTUI_CompletionValidation(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.pipelineScreen.active = true

	m.pipelineScreen.ApplySnapshot(orchestrator.ObsSnapshot{
		Terminal: orchestrator.TerminalState{
			Done:   true,
			Result: orchestrator.Result{WorkerValidation: "✓ tests pass\n✓ build succeeds"},
		},
	}, m.width)

	if m.pipelineScreen.content != ContentCompletion {
		t.Errorf("expected ContentCompletion, got %d", m.pipelineScreen.content)
	}
	if m.pipelineScreen.workerValidation == "" {
		t.Error("expected workerValidation to be set")
	}
}

// TestTUI_PipelineViewPurity locks the Stage 0a invariant: Model.View() is a pure
// render. It must be idempotent (two consecutive renders identical) and the ctrl+C
// footer state must flow through the View render parameter, not a field that View
// mutates at render time (the old model.go:706 `pipelineScreen.ctrlCPending = …` bug).
func TestTUI_PipelineViewPurity(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.pipelineScreen.active = true
	m.width = 120
	m.height = 40
	m.recalculateLayout()

	// Idempotent: rendering twice with no intervening Update yields identical output.
	if out1, out2 := viewString(m), viewString(m); out1 != out2 {
		t.Fatal("View() is not idempotent: two consecutive renders differ")
	}

	// ctrlCPending flows through the render path, not a stored/mutated field.
	m.ctrlCPending = false
	if v := viewString(m); !strings.Contains(v, "[^C] cancel") {
		t.Errorf("ctrlCPending=false: expected '[^C] cancel' footer, got:\n%s", v)
	}
	m.ctrlCPending = true
	if v := viewString(m); !strings.Contains(v, "EXIT") {
		t.Errorf("ctrlCPending=true: expected 'EXIT' footer, got:\n%s", v)
	}
}

func TestFormatTokens(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "-"},
		{500, "500"},
		{1200, "1.2k"},
		{12400, "12k"},
		{128000, "128k"},
		{1500000, "1.5M"},
	}
	for _, tt := range tests {
		got := formatTokens(tt.n)
		if got != tt.want {
			t.Errorf("formatTokens(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestTUI_TickRefreshesView(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.pipelineScreen.goal = "test tick"

	// tickMsg should return another tick command during pipeline
	result, cmd := m.Update(tickMsg(time.Now()))
	model := result.(Model)

	if model.state != StatePipeline {
		t.Errorf("expected StatePipeline, got %d", model.state)
	}
	if cmd == nil {
		t.Error("expected tick to return another tick command during pipeline")
	}
}

func TestTUI_TickStopsAfterPrompt(t *testing.T) {
	m := testModel()
	m.state = StatePrompt

	// tickMsg should not schedule another tick when not in pipeline
	_, cmd := m.Update(tickMsg(time.Now()))
	if cmd != nil {
		t.Error("expected no tick command when not in pipeline state")
	}
}

func TestTUI_StreamingOutput(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.pipelineScreen.goal = "stream test"
	m.width = 120
	m.height = 40

	_ = m.pipelineScreen.Start("stream test") // activates streaming console
	stream := orchestrator.NewStreamRing(200)
	m.pipelineScreen.streamBuf = stream
	stream.SetAgent("researcher")

	// Drain a completed line + a streaming delta through DrainStreamUpdates.
	updates := make(chan orchestrator.StreamEntry, 3)
	updates <- orchestrator.StreamEntry{Kind: orchestrator.EntryText, Text: "Completed line\n"}
	updates <- orchestrator.StreamEntry{Kind: orchestrator.EntryDelta, Text: "Partial in progress"}
	close(updates)
	m.pipelineScreen.DrainStreamUpdates(updates)

	m.recalculateLayout()

	// Completed line goes to transcript (visible in View).
	view := viewString(m)
	if !strings.Contains(view, "Completed line") {
		t.Error("completed line should appear in transcript View()")
	}
	// Streaming delta appears in the streaming console partial.
	if !strings.Contains(view, "Partial in progress") {
		t.Error("expected streaming delta to appear in streaming console partial")
	}

	// Completed line must be in the timeline.
	if !m.pipelineScreen.timeline.HasContent() {
		t.Error("expected timeline to have content after ingesting a completed line")
	}
}

func TestTUI_StreamingOutputReset(t *testing.T) {
	stream := orchestrator.NewStreamRing(200)

	// Simulate first agent
	stream.SetAgent("researcher")
	stream.AppendText("researcher output line\n")

	agentID, lines, _ := stream.SnapshotCompat()
	if agentID != "researcher" {
		t.Errorf("expected agent 'researcher', got %q", agentID)
	}
	if len(lines) == 0 {
		t.Fatal("expected stream lines from researcher")
	}

	// Simulate second agent — buffer should reset
	stream.SetAgent("architect")

	agentID2, lines2, _ := stream.SnapshotCompat()
	if agentID2 != "architect" {
		t.Errorf("expected agent 'architect', got %q", agentID2)
	}
	if len(lines2) != 0 {
		t.Errorf("expected stream buffer cleared on new agent, got %d lines", len(lines2))
	}
}

func TestStreamBuffer_TokenAccumulation(t *testing.T) {
	stream := orchestrator.NewStreamRing(200)
	stream.SetAgent("researcher")

	// Simulate token-level writes (each content_block_delta is a few chars)
	stream.AppendText("I")
	stream.AppendText("'ll")
	stream.AppendText(" analyze")
	stream.AppendText(" the")
	stream.AppendText(" request")

	_, lines, _ := stream.SnapshotCompat()
	if len(lines) != 1 {
		t.Errorf("expected 1 line from token-level writes, got %d: %v", len(lines), lines)
	}
	if lines[0] != "I'll analyze the request" {
		t.Errorf("unexpected accumulated line: %q", lines[0])
	}

	// Now write a newline to start a new line
	stream.AppendText(".\nNext line here")

	_, lines, _ = stream.SnapshotCompat()
	if len(lines) != 2 {
		t.Errorf("expected 2 lines after newline, got %d: %v", len(lines), lines)
	}
	if lines[0] != "I'll analyze the request." {
		t.Errorf("unexpected first line: %q", lines[0])
	}
	if lines[1] != "Next line here" {
		t.Errorf("unexpected second line: %q", lines[1])
	}
}

func TestTUI_NewRunClearsStaleState(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentCompletion
	m.pipelineScreen.goal = "previous task"

	// Simulate stale state from a previous run
	m.pipelineScreen.agents = []AgentRow{
		{ID: "researcher", State: AgentStateDone},
		{ID: "architect", State: AgentStateFailed},
	}
	m.pipelineScreen.lastErr = fmt.Errorf("architect failed")
	m.pipelineScreen.finalPlan = "# Old Plan"
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.workerValidation = "old validation"

	// Press Ctrl+N to start new run
	result, _ := sendCtrl(m, 'n')
	model := result.(Model)

	if model.state != StatePrompt {
		t.Fatalf("expected StatePrompt, got %d", model.state)
	}

	// All stale state must be cleared
	if len(model.pipelineScreen.agents) != 0 {
		t.Errorf("expected agents cleared, got %d agents", len(model.pipelineScreen.agents))
	}
	if model.pipelineScreen.lastErr != nil {
		t.Errorf("expected lastErr cleared, got %v", model.pipelineScreen.lastErr)
	}
	if model.pipelineScreen.hasPlan {
		t.Error("expected hasPlan cleared")
	}
	if model.pipelineScreen.workerValidation != "" {
		t.Error("expected workerValidation cleared")
	}
	if model.pipelineScreen.finalPlan != "" {
		t.Error("expected finalPlan cleared")
	}

	// Goal should be preserved in prompt
	if !strings.Contains(model.promptScreen.Value(), "previous task") {
		t.Errorf("expected prompt pre-filled, got %q", model.promptScreen.Value())
	}
}

func TestTUI_RestartClearsErrorAndAgents(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentCompletion
	m.pipelineScreen.goal = "task"
	m.pipelineScreen.agents = []AgentRow{{ID: "researcher", State: AgentStateDone}}
	m.pipelineScreen.lastErr = fmt.Errorf("old error")

	// Press Ctrl+N, then submit new prompt via Enter
	result, _ := sendCtrl(m, 'n')
	model := result.(Model)
	model.promptScreen.SetValue("new task")

	result2, _ := sendKey(model, tea.KeyEnter)
	model2 := result2.(Model)

	if model2.state != StatePipeline {
		t.Fatalf("expected StatePipeline, got %d", model2.state)
	}
	if len(model2.pipelineScreen.agents) != 0 {
		t.Errorf("expected agents cleared on new pipeline start, got %d", len(model2.pipelineScreen.agents))
	}
	if model2.pipelineScreen.lastErr != nil {
		t.Errorf("expected lastErr cleared on new pipeline start, got %v", model2.pipelineScreen.lastErr)
	}
	model2.cancel()
}

func TestTUI_PlanGateBlocksOverwrite(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming

	// Snapshot with gate open
	m.pipelineScreen.ApplySnapshot(orchestrator.ObsSnapshot{
		HasGate: true,
		Gate: orchestrator.GateRequest{
			Position:          orchestrator.GateAfterDeliberation,
			FinalPlanMarkdown: "# Plan\n\n## Goal\nTest",
		},
	}, m.width)

	if m.pipelineScreen.content != ContentHumanGate {
		t.Fatalf("expected ContentHumanGate, got %d", m.pipelineScreen.content)
	}
	if !m.pipelineScreen.awaitingPlanDecision {
		t.Fatal("expected awaitingPlanDecision=true")
	}

	// Snapshot with a stale phase change but gate still open
	m.pipelineScreen.ApplySnapshot(orchestrator.ObsSnapshot{
		Phase:   orchestrator.PhaseExecuting,
		HasGate: true,
		Gate: orchestrator.GateRequest{
			Position:          orchestrator.GateAfterDeliberation,
			FinalPlanMarkdown: "# Plan\n\n## Goal\nTest",
		},
	}, m.width)

	// Gate must NOT be overwritten
	if m.pipelineScreen.content != ContentHumanGate {
		t.Errorf("gate was overwritten by stale phase change: content=%d", m.pipelineScreen.content)
	}
	// Phase should not be updated while gate is active
	if m.pipelineScreen.phase == orchestrator.PhaseExecuting {
		t.Error("phase was updated despite awaitingPlanDecision being true")
	}
}

func TestTUI_EditorReturn(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentHumanGate
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = "# Plan\n\nOriginal content"
	// channel removed — test checks state only

	// Write a modified plan to a temp file
	tmpFile, err := os.CreateTemp("", "test-plan-*.md")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	modifiedPlan := "# Plan\n\nModified content with changes"
	if _, err := tmpFile.WriteString(modifiedPlan); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	tmpFile.Close()

	m.pipelineScreen.editorFilePath = tmpFile.Name()

	// Simulate editor return — should show confirmation prompt, NOT immediate DecisionEdit
	result, cmd := m.Update(editorReturnMsg{err: nil})
	model := result.(Model)

	if cmd != nil {
		msg := cmd()
		result, _ = model.Update(msg)
		model = result.(Model)
	}

	if model.pipelineScreen.content != ContentEditConfirm {
		t.Errorf("expected ContentEditConfirm after editor return with changes, got %d", model.pipelineScreen.content)
	}
	if model.pipelineScreen.editConfirm.pending != modifiedPlan {
		t.Errorf("expected pendingEditContent = modified plan, got %q", model.pipelineScreen.editConfirm.pending)
	}
	// channel removed — no decision should be sent yet (confirmation is pending), checked by state only
}

// TestTUI_DrainLoopPlanGate exercises the ObsStore-based snapshot path:
// agent-done and gate-open are written to the store, then obsNotifyMsg fires,
// leaving the model in ContentHumanGate.
func TestTUI_DrainLoopPlanGate(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.pipelineScreen.active = true
	m.pipelineScreen.agents = []AgentRow{{ID: "architect", State: AgentStateRunning, StartedAt: time.Now()}}

	obs := orchestrator.NewObsStore()
	ctrl := orchestrator.NewControl(obs)
	m.obs = obs
	m.ctrl = ctrl

	planMD := "# Plan\n\n## Goal\nAdd X.\n\n## Work Packages\n\n### 1. Step 1"

	// Populate obs state: agent done, then gate opened.
	obs.AgentStarted("architect", orchestrator.AgentMeta{})
	obs.AgentDone("architect", harness.TokenUsage{Input: 100, Output: 50})
	obs.GateOpened(orchestrator.GateRequest{
		Position:          orchestrator.GateAfterDeliberation,
		FinalPlanMarkdown: planMD,
		PlanFilePath:      "/tmp/plan.md",
	})

	// Fire obsNotifyMsg — ApplySnapshot detects the gate and switches to ContentHumanGate.
	result, cmd := m.Update(obsNotifyMsg{})
	model := result.(Model)

	if model.pipelineScreen.content != ContentHumanGate {
		t.Errorf("expected ContentHumanGate after obsNotifyMsg, got %d", model.pipelineScreen.content)
	}
	if !model.pipelineScreen.awaitingPlanDecision {
		t.Error("expected awaitingPlanDecision=true")
	}
	if !model.pipelineScreen.hasPlan {
		t.Error("expected hasPlan=true")
	}
	if model.pipelineScreen.finalPlan != planMD {
		t.Errorf("expected finalPlan to be set, got %q", model.pipelineScreen.finalPlan)
	}
	// cmd should be non-nil: notifyCmd(obs.NotifyCh()) since terminal.Done=false
	if cmd == nil {
		t.Error("expected non-nil cmd (notifyCmd)")
	}
}

// TestTUI_ChannelCloseDoesNotOverwriteGate verifies that when the pipeline
// finishes (terminal.Done=true) while awaitingPlanDecision, the gate is NOT
// overwritten by the Terminal.Done branch in ApplySnapshot.
func TestTUI_ChannelCloseDoesNotOverwriteGate(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.active = true

	obs := orchestrator.NewObsStore()
	m.obs = obs
	m.ctrl = orchestrator.NewControl(obs)

	// Open the gate first so awaitingPlanDecision is set.
	obs.GateOpened(orchestrator.GateRequest{
		Position:          orchestrator.GateAfterDeliberation,
		FinalPlanMarkdown: "# Plan\n\n## Goal\nTest",
	})
	result, _ := m.Update(obsNotifyMsg{})
	m = result.(Model)

	if m.pipelineScreen.content != ContentHumanGate {
		t.Fatalf("expected ContentHumanGate after gate opened, got %d", m.pipelineScreen.content)
	}
	if !m.pipelineScreen.awaitingPlanDecision {
		t.Fatal("expected awaitingPlanDecision=true")
	}

	// Now simulate terminal done — the gate guard must prevent overwrite.
	obs.Finished(orchestrator.Result{Status: orchestrator.StatusFailed}, nil)
	result, _ = m.Update(obsNotifyMsg{})
	model := result.(Model)

	if model.pipelineScreen.content != ContentHumanGate {
		t.Errorf("terminal done overwrote gate: expected ContentHumanGate, got %d", model.pipelineScreen.content)
	}
}

// TestTUI_DrainLoopChannelCloseAfterGate is skipped: the ObsStore path does not
// have a channel-close race because the pipeline goroutine blocks on the gate
// (ctrl.Gate blocks until the user decides), so terminal.Done cannot be set
// while the gate is open. The gate-guard invariant is covered by
// TestTUI_ChannelCloseDoesNotOverwriteGate.
func TestTUI_DrainLoopChannelCloseAfterGate(t *testing.T) {
	t.Skip("skipped: channel-close race not possible with ObsStore/Control gate blocking")
}


func TestTUI_ShiftEnterNewline(t *testing.T) {
	m := testModel()
	m.promptScreen.SetValue("line one")

	// Shift+Enter should NOT submit — should stay in StatePrompt
	result, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	model := result.(Model)

	if model.state != StatePrompt {
		t.Errorf("expected StatePrompt after Shift+Enter, got %d", model.state)
	}

	// Alt+Enter should also NOT submit
	result2, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	model2 := result2.(Model)

	if model2.state != StatePrompt {
		t.Errorf("expected StatePrompt after Alt+Enter, got %d", model2.state)
	}

	// Plain Enter SHOULD submit
	result3, _ := sendKey(model2, tea.KeyEnter)
	model3 := result3.(Model)

	if model3.state != StatePipeline {
		t.Errorf("expected StatePipeline after plain Enter, got %d", model3.state)
	}
	model3.cancel()
}

func TestTUI_ChatResponse(t *testing.T) {
	t.Skip("skipped: ChatResponse flow replaced by HumanChatMode in v6")
}


func TestTUI_ReviewTokenAccumulation(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.chatHistory = []ChatEntry{{Role: ChatRoleUser, Text: "q1"}}

	// Architect appears running first (snapshot-based registration).
	m.pipelineScreen.ApplySnapshot(orchestrator.ObsSnapshot{
		Agents: []orchestrator.AgentSnapshot{
			{AgentID: "architect", Status: "running"},
		},
	}, m.width)

	// Architect done — review tokens should accumulate because chatHistory is non-empty.
	m.pipelineScreen.ApplySnapshot(orchestrator.ObsSnapshot{
		Agents: []orchestrator.AgentSnapshot{
			{AgentID: "architect", Status: "done", Input: 1000, Output: 500},
		},
	}, m.width)

	if m.pipelineScreen.reviewTokensIn != 1000 {
		t.Errorf("reviewTokensIn = %d, want 1000", m.pipelineScreen.reviewTokensIn)
	}
	if m.pipelineScreen.reviewTokensOut != 500 {
		t.Errorf("reviewTokensOut = %d, want 500", m.pipelineScreen.reviewTokensOut)
	}
}


// TestApplySnapshot_TerminalErrShowsInCompletion verifies that a pipeline failure
// reported via obs.Finished is visible in the completion screen — the real path
// through obsNotifyMsg → ApplySnapshot (not ApplyEvent, which is never called).
func TestApplySnapshot_TerminalErrShowsInCompletion(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.active = true

	obs := orchestrator.NewObsStore()
	m.obs = obs
	m.ctrl = orchestrator.NewControl(obs)

	runErr := errors.New("research: read plan: model session x completed but did not write a plan file")
	obs.Finished(orchestrator.Result{Status: orchestrator.StatusFailed}, runErr)
	result, _ := m.Update(obsNotifyMsg{})
	model := result.(Model)

	if model.pipelineScreen.lastErr == nil {
		t.Fatal("lastErr is nil after failed pipeline; ApplySnapshot must copy snap.Terminal.Err")
	}
	out := model.pipelineScreen.viewCompletion(80)
	if !strings.Contains(out, "Error:") {
		t.Errorf("viewCompletion missing 'Error:' line:\n%s", out)
	}
	if !strings.Contains(model.pipelineScreen.lastErr.Error(), "did not write a plan file") {
		t.Errorf("unexpected lastErr content: %v", model.pipelineScreen.lastErr)
	}
}

// TestApplySnapshot_AgentFailedErrShowsInCompletion verifies that an agent
// failure error stored in AgentSnapshot.Error reaches s.lastErr via ApplySnapshot.
func TestApplySnapshot_AgentFailedErrShowsInCompletion(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.active = true

	obs := orchestrator.NewObsStore()
	m.obs = obs
	m.ctrl = orchestrator.NewControl(obs)

	agentErr := errors.New("research: read plan: model did not write a plan file")
	obs.AgentStarted("researcher", orchestrator.AgentMeta{ModelRef: "test"})
	// First tick: registers agent in knownAgents as "running".
	result, _ := m.Update(obsNotifyMsg{})
	m = result.(Model)

	obs.AgentFailed("researcher", agentErr)
	// Second tick: "running" → "failed" transition sets lastErr.
	result, _ = m.Update(obsNotifyMsg{})
	model := result.(Model)

	if model.pipelineScreen.lastErr == nil {
		t.Fatal("lastErr is nil after agent failure; AgentSnapshot.Error must propagate through ApplySnapshot")
	}
	if !strings.Contains(model.pipelineScreen.lastErr.Error(), "did not write a plan file") {
		t.Errorf("unexpected lastErr: %v", model.pipelineScreen.lastErr)
	}
}
