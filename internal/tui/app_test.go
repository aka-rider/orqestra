package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/textarea"
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
	m := NewModel(engine, "test.yaml")
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

	event := orchestrator.Event{
		Type: orchestrator.EventGateRequest,
		Gate: orchestrator.GateRequest{
			Position:          orchestrator.GateAfterDeliberation,
			FinalPlanMarkdown: "# Plan\n\n## Goal\nAdd feature X\n\n## Work Packages\n\n### 1. Step 1",
		},
	}

	m.pipelineScreen.ApplyEvent(event, m.width)

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

func TestTUI_PlanEditOpensExternalEditor(t *testing.T) {
	t.Skip("skipped: PlanEditOpensExternalEditor flow replaced by HumanChatMode in v6")
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentHumanGate
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nOriginal"
	m.pipelineScreen.planFilePath = "/tmp/test-plan.md"

	// Press Ctrl+E — should emit OpenExternalEditorIntent, not ContentPlanEdit
	result, _ := sendCtrl(m, 'e')
	model := result.(Model)

	// Content mode must NOT have changed to a removed state
	if model.pipelineScreen.content != ContentHumanGate {
		t.Errorf("expected ContentHumanGate (unchanged), got %d", model.pipelineScreen.content)
	}
	if !model.pipelineScreen.editorRunning {
		t.Error("expected editorRunning=true after Ctrl+E")
	}
}

func TestTUI_PlanEditCtrlShiftEOpensExternalEditor(t *testing.T) {
	t.Skip("skipped: PlanEditCtrlShiftEOpensExternalEditor flow replaced by HumanChatMode in v6")
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentHumanGate
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.planFilePath = "/tmp/test-plan.md"

	// ctrl+shift+e should also open external editor
	result, _ := sendCtrlShift(m, 'e')
	model := result.(Model)

	if !model.pipelineScreen.editorRunning {
		t.Error("expected editorRunning=true after Ctrl+Shift+E")
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
	m.pipelineScreen.content = ContentUserQuestion
	m.pipelineScreen.question = newUserQuestion(mcp.ToolCall{
		Question: "Pick one",
		Options:  []mcp.ToolOption{{Label: "Yes"}, {Label: "No"}},
	}, 80)
	m.pipelineScreen.hasQuestion = true

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

func TestTUI_AgentNavigation(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.pipelineScreen.agents = []AgentRow{
		{ID: "architect", State: AgentStateRunning},
	}

	// Press Alt+1 to view architect history
	result, _ := sendAlt(m, '1')
	model := result.(Model)

	if model.pipelineScreen.content != ContentAgentHistory {
		t.Errorf("expected ContentAgentHistory, got %d", model.pipelineScreen.content)
	}
	if model.pipelineScreen.focusedAgent != 1 {
		t.Errorf("expected focusedAgent=1, got %d", model.pipelineScreen.focusedAgent)
	}
}

func TestTUI_AgentNavBack(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentAgentHistory
	m.pipelineScreen.focusedAgent = 1
	m.pipelineScreen.agents = []AgentRow{{ID: "architect", State: AgentStateDone}}

	// Press Esc to go back
	result, _ := sendKey(m, tea.KeyEscape)
	model := result.(Model)

	if model.pipelineScreen.content != ContentStreaming {
		t.Errorf("expected ContentStreaming after Esc, got %d", model.pipelineScreen.content)
	}
	if model.pipelineScreen.focusedAgent != 0 {
		t.Errorf("expected focusedAgent=0, got %d", model.pipelineScreen.focusedAgent)
	}
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

	// AgentStarted
	m.pipelineScreen.ApplyEvent(orchestrator.Event{
		Type:    orchestrator.EventAgentStarted,
		AgentID: "researcher",
	}, m.width)

	if len(m.pipelineScreen.agents) != 1 || m.pipelineScreen.agents[0].State != AgentStateRunning {
		t.Errorf("expected 1 agent running, got %+v", m.pipelineScreen.agents)
	}

	// AgentDone
	m.pipelineScreen.ApplyEvent(orchestrator.Event{
		Type:    orchestrator.EventAgentDone,
		AgentID: "researcher",
	}, m.width)

	if m.pipelineScreen.agents[0].State != AgentStateDone {
		t.Errorf("expected agent state 'done', got %q", m.pipelineScreen.agents[0].State)
	}
}

func TestTUI_FullDashboard(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.pipelineScreen.agents = []AgentRow{
		{ID: "architect", State: AgentStateRunning},
	}

	// Press Ctrl+D to toggle dashboard
	result, _ := sendCtrl(m, 'd')
	model := result.(Model)

	if !model.pipelineScreen.showDashboard {
		t.Error("expected showDashboard=true")
	}

	// View should contain dashboard content
	view := viewString(model)
	if !strings.Contains(view, "architect") {
		t.Error("expected dashboard to show agent names")
	}

	// Press Esc to return
	result2, _ := sendKey(model, tea.KeyEscape)
	model2 := result2.(Model)

	if model2.pipelineScreen.showDashboard {
		t.Error("expected showDashboard=false after Esc")
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

	event := orchestrator.Event{
		Type:             orchestrator.EventComplete,
		WorkerValidation: "✓ tests pass\n✓ build succeeds",
	}

	m.pipelineScreen.ApplyEvent(event, m.width)

	if m.pipelineScreen.content != ContentCompletion {
		t.Errorf("expected ContentCompletion, got %d", m.pipelineScreen.content)
	}
	if !m.pipelineScreen.hasValidation {
		t.Error("expected hasValidation=true")
	}
}

func TestTUI_CompletionMergeFailureBanner(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming

	m.pipelineScreen.ApplyEvent(orchestrator.Event{
		Type:              orchestrator.EventMergeError,
		MergeError:        "worktree: merge \"orqestra-run-1\": exit status 2",
		MergeBranch:       "orqestra-run-1",
		MergeWorktreePath: "/tmp/orqestra/worktree",
	}, m.width)
	m.pipelineScreen.ApplyEvent(orchestrator.Event{
		Type:             orchestrator.EventComplete,
		WorkerValidation: "✓ tests pass",
		Status:           orchestrator.StatusFailed,
	}, m.width)

	view := m.pipelineScreen.viewCompletion(m.width)
	if !strings.Contains(view, "Merge failed — manual recovery required") {
		t.Fatalf("completion view missing merge failure banner:\n%s", view)
	}
	if !strings.Contains(view, "Preserved branch: orqestra-run-1") {
		t.Fatalf("completion view missing preserved branch:\n%s", view)
	}
	if !strings.Contains(view, "Preserved worktree: /tmp/orqestra/worktree") {
		t.Fatalf("completion view missing preserved worktree:\n%s", view)
	}
	if !strings.Contains(view, "git merge orqestra-run-1") {
		t.Fatalf("completion view missing manual merge command:\n%s", view)
	}
}

func TestTUI_PgUpPgDown(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.pipelineScreen.goal = "test scroll"
	// Initialize layout so viewports have dimensions
	m.width = 120
	m.height = 40
	m.recalculateLayout()

	// Set content taller than viewport to enable scrolling
	var longContent strings.Builder
	for i := 0; i < 100; i++ {
		longContent.WriteString(fmt.Sprintf("line %d\n", i))
	}
	m.pipelineScreen.contentVP.SetContent(longContent.String())

	// PgDown should change viewport offset
	result, _ := sendKey(m, tea.KeyPgDown)
	model := result.(Model)
	if model.pipelineScreen.contentVP.YOffset() == 0 {
		t.Error("expected viewport YOffset > 0 after PgDown")
	}

	// PgUp should return to top
	result2, _ := sendKey(model, tea.KeyPgUp)
	model2 := result2.(Model)
	if model2.pipelineScreen.contentVP.YOffset() != 0 {
		t.Errorf("expected YOffset=0 after PgUp from first page, got %d", model2.pipelineScreen.contentVP.YOffset())
	}

	// PgUp at 0 should stay at 0
	result3, _ := sendKey(model2, tea.KeyPgUp)
	model3 := result3.(Model)
	if model3.pipelineScreen.contentVP.YOffset() != 0 {
		t.Errorf("expected YOffset=0, got %d", model3.pipelineScreen.contentVP.YOffset())
	}
}

func TestTUI_SidebarTokens(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.pipelineScreen.active = true
	m.width = 120
	m.height = 40
	m.pipelineScreen.agents = []AgentRow{
		{ID: "researcher", State: AgentStateDone, Elapsed: 3 * time.Second, InputTokens: 1218, OutputTokens: 402},
		{ID: "architect", State: AgentStateRunning, StartedAt: time.Now().Add(-24 * time.Second), InputTokens: 0, OutputTokens: 0, ModelDisplay: "claude-opus-4"},
	}
	m.recalculateLayout()
	m.pipelineScreen.SyncViewports()

	view := viewString(m)

	// Status bar shows agent chain with state icons
	if !strings.Contains(view, "✓rese") {
		t.Error("expected status bar to show done researcher with ✓ icon")
	}
	if !strings.Contains(view, "▶arch") {
		t.Error("expected status bar to show running architect with ▶ icon")
	}
}

func TestTUI_DashboardTokens(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.pipelineScreen.showDashboard = true
	m.width = 120
	m.height = 40
	m.pipelineScreen.agents = []AgentRow{
		{ID: "researcher", State: AgentStateDone, Elapsed: 3 * time.Second, InputTokens: 1218, OutputTokens: 402, ModelDisplay: "claude-opus-4"},
	}
	m.recalculateLayout()
	m.pipelineScreen.SyncViewports()

	view := viewString(m)
	// New dashboard shows agent cards with state icons and model names
	if !strings.Contains(view, "researcher") {
		t.Error("expected dashboard to show agent name 'researcher'")
	}
	if !strings.Contains(view, "✓") {
		t.Error("expected dashboard to show done icon ✓")
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

	// Create a shared stream buffer (like the orchestrator would)
	stream := orchestrator.NewStreamRing(200)
	m.pipelineScreen.streamBuf = stream

	// Simulate agent start + streaming output via the shared buffer
	stream.SetAgent("researcher")
	stream.AppendText("Analyzing prompt...\nProcessing request...")

	m.recalculateLayout()
	m.pipelineScreen.SyncViewports()

	// Verify the view renders the streamed content
	view := viewString(m)
	if !strings.Contains(view, "Analyzing prompt") {
		t.Error("expected streaming output to appear in view")
	}
	if !strings.Contains(view, "Processing request") {
		t.Error("expected second line of streaming output in view")
	}
	if !strings.Contains(view, "researcher") {
		t.Error("expected agent name in streaming view")
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
	m.pipelineScreen.hasValidation = true

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
	if model.pipelineScreen.hasValidation {
		t.Error("expected hasValidation cleared")
	}
	if model.pipelineScreen.showDashboard {
		t.Error("expected showDashboard cleared")
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

func TestTUI_ConfigNameInHeader(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.pipelineScreen.goal = "test"
	m.width = 120
	m.height = 40

	view := viewString(m)
	if !strings.Contains(view, "test.yaml") {
		t.Error("expected config name 'test.yaml' in pipeline header")
	}
}

func TestTUI_PlanGateBlocksOverwrite(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming

	// Simulate gate event
	m.pipelineScreen.ApplyEvent(orchestrator.Event{
		Type: orchestrator.EventGateRequest,
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

	// Simulate a stale EventPhaseChange arriving after the gate
	m.pipelineScreen.ApplyEvent(orchestrator.Event{
		Type:  orchestrator.EventPhaseChange,
		Phase: orchestrator.PhaseExecuting,
	}, m.width)

	// Gate must NOT be overwritten
	if m.pipelineScreen.content != ContentHumanGate {
		t.Errorf("gate was overwritten by stale EventPhaseChange: content=%d", m.pipelineScreen.content)
	}
	// Phase should not be updated while gate is active
	if m.pipelineScreen.phase == orchestrator.PhaseExecuting {
		t.Error("phase was updated despite awaitingPlanDecision being true")
	}
}

func TestTUI_PlanReviewComment(t *testing.T) {
	t.Skip("skipped: PlanReviewComment flow replaced by HumanChatMode in v6")
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentHumanGate
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nTest"
	// channel removed — test checks state only

	// Initialize comment textarea
	m.pipelineScreen.planComment = textarea.New()
	m.pipelineScreen.planComment.SetWidth(80)
	m.pipelineScreen.planComment.SetHeight(2)
	m.pipelineScreen.planComment.CharLimit = 1024
	m.pipelineScreen.planComment.Focus()
	m.pipelineScreen.hasPlanComment = true
	m.pipelineScreen.planComment.SetValue("please add error handling")

	// Press Enter to submit comment
	result, cmd := sendKey(m, tea.KeyEnter)
	model := result.(Model)

	if model.pipelineScreen.content != ContentStreaming {
		t.Errorf("expected ContentStreaming after comment submit, got %d", model.pipelineScreen.content)
	}
	if cmd == nil {
		t.Error("expected non-nil cmd (waitForEvent)")
	}
}

func TestTUI_PlanReviewExternalEditor(t *testing.T) {
	t.Skip("skipped: External editor flow replaced by HumanChatMode in v6")
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentHumanGate
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nTest"
	m.pipelineScreen.planFilePath = "/tmp/test-plan.md"
	m.pipelineScreen.hasPlanComment = true
	m.pipelineScreen.planComment = textarea.New()

	// Press Ctrl+Shift+E to open external editor
	result, cmd := sendCtrlShift(m, 'e')
	model := result.(Model)

	if !model.pipelineScreen.editorRunning {
		t.Error("expected editorRunning=true after Ctrl+Shift+E")
	}
	if cmd == nil {
		t.Error("expected non-nil cmd (ExecProcess)")
	}
}

func TestTUI_PlanReviewGlamour(t *testing.T) {
	t.Skip("skipped: PlanReviewGlamour flow replaced by HumanChatMode in v6")
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentHumanGate
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nAdd feature X.\n\n## Work Packages\n\n### 1. Step 1\n\n- item a\n- item b\n"
	m.width = 120
	m.height = 40

	view := m.pipelineScreen.viewPlanReview(80)
	if view == m.pipelineScreen.finalPlan {
		t.Error("expected glamour to transform the markdown, got raw input back")
	}
	if !strings.Contains(view, "Plan") {
		t.Error("expected rendered output to contain 'Plan'")
	}
}

func TestTUI_EditorReturn(t *testing.T) {
	t.Skip("skipped: EditorReturn flow replaced by HumanChatMode in v6")
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

	m.pipelineScreen.planFilePath = tmpFile.Name()

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
	if model.pipelineScreen.pendingEditContent != modifiedPlan {
		t.Errorf("expected pendingEditContent = modified plan, got %q", model.pipelineScreen.pendingEditContent)
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

// TestTUI_PlanReviewTextareaGuard verifies that bare letter keys go to the
// comment textarea and do NOT trigger action shortcuts when the textarea is focused.
// Regression test for: typing in plan comment caused gate to skip.
func TestTUI_PlanReviewTextareaGuard(t *testing.T) {
	t.Skip("skipped: textarea guard replaced by HumanChatMode nil-guard in v6")
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentHumanGate
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nTest"
	m.pipelineScreen.hasPlanComment = true
	m.pipelineScreen.planComment = textarea.New()
	m.pipelineScreen.planComment.SetWidth(80)
	m.pipelineScreen.planComment.SetHeight(2)
	m.pipelineScreen.planComment.Focus()
	m.pipelineScreen.awaitingPlanDecision = true
	// channel removed — test checks state only

	// Type letters that used to be action shortcuts: a, e, s, d
	for _, ch := range []string{"a", "e", "s", "d"} {
		result, _ := sendRune(m, ch)
		model := result.(Model)

		if model.pipelineScreen.content != ContentHumanGate {
			t.Errorf("typing %q switched content to %d — expected to stay in ContentHumanGate", ch, model.pipelineScreen.content)
		}
		if model.pipelineScreen.PendingIntent != nil {
			t.Errorf("typing %q triggered intent %T — expected nil (key should go to textarea)", ch, model.pipelineScreen.PendingIntent)
		}
	}
}

// TestTUI_PlanReviewCtrlAApproves verifies Ctrl+A approves plan even when
// comment textarea is focused.
func TestTUI_PlanReviewCtrlAApproves(t *testing.T) {
	t.Skip("skipped: Ctrl+A approve flow replaced by HumanChatMode in v6")
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentHumanGate
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nTest"
	m.pipelineScreen.hasPlanComment = true
	m.pipelineScreen.planComment = textarea.New()
	m.pipelineScreen.planComment.SetWidth(80)
	m.pipelineScreen.planComment.SetHeight(2)
	m.pipelineScreen.planComment.Focus()
	m.pipelineScreen.awaitingPlanDecision = true
	// channel removed — test checks state only

	// Ctrl+A should approve even with textarea active
	result, _ := sendCtrl(m, 'a')
	model := result.(Model)

	if model.pipelineScreen.content != ContentStreaming {
		t.Errorf("expected ContentStreaming after Ctrl+A approve, got %d", model.pipelineScreen.content)
	}
}

// TestTUI_PlanReviewEscDismissesTextarea verifies Esc blurs the comment textarea
// without cancelling the plan review.
func TestTUI_PlanReviewEscDismissesTextarea(t *testing.T) {
	t.Skip("skipped: Esc dismisses textarea flow replaced by HumanChatMode in v6")
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentHumanGate
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.hasPlanComment = true
	m.pipelineScreen.planComment = textarea.New()
	m.pipelineScreen.planComment.SetWidth(80)
	m.pipelineScreen.planComment.SetHeight(2)
	m.pipelineScreen.planComment.Focus()

	result, _ := sendKey(m, tea.KeyEscape)
	model := result.(Model)

	if model.pipelineScreen.hasPlanComment {
		t.Error("expected hasPlanComment=false after Esc")
	}
	if model.pipelineScreen.content != ContentHumanGate {
		t.Errorf("expected ContentHumanGate after Esc, got %d", model.pipelineScreen.content)
	}
}

// TestTUI_GlobalKeysBlockedInPlanReview ensures "d" and number keys
// are routed to the comment textarea, not to global handlers.
func TestTUI_GlobalKeysBlockedInPlanReview(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentHumanGate
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.hasPlanComment = true
	m.pipelineScreen.planComment = textarea.New()
	m.pipelineScreen.planComment.SetWidth(80)
	m.pipelineScreen.planComment.SetHeight(2)
	m.pipelineScreen.planComment.Focus()
	m.pipelineScreen.agents = []AgentRow{{ID: "architect", State: AgentStateDone}}

	// Press "d" — must NOT toggle dashboard
	result, _ := sendRune(m, "d")
	model := result.(Model)

	if model.pipelineScreen.showDashboard {
		t.Error("pressing 'd' in ContentHumanGate toggled dashboard instead of typing in comment textarea")
	}

	// Press "1" — must NOT switch to agent history
	result2, _ := sendRune(model, "1")
	model2 := result2.(Model)

	if model2.pipelineScreen.content != ContentHumanGate {
		t.Errorf("pressing '1' in ContentHumanGate switched content to %d", model2.pipelineScreen.content)
	}
}

// TestTUI_PlanReviewInputHeight verifies that the content height accounts
// for the taller input zone in ContentHumanGate mode.
func TestTUI_PlanReviewInputHeight(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentHumanGate
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nTest\n\n## Work Packages\n\n### 1. Do thing"
	m.width = 120
	m.height = 40
	m.pipelineScreen.hasPlanComment = true
	m.pipelineScreen.planComment = textarea.New()
	m.pipelineScreen.planComment.SetWidth(80)
	m.pipelineScreen.planComment.SetHeight(2)

	view := viewString(m)
	lines := strings.Split(view, "\n")

	// The view must not exceed the terminal height.
	// Allow a 1-line tolerance for trailing newline.
	if len(lines) > m.height+1 {
		t.Errorf("plan review view exceeds terminal height: %d lines for %d-row terminal", len(lines), m.height)
	}
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
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentHumanGate
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nOriginal"
	m.pipelineScreen.hasPlanComment = true
	m.pipelineScreen.planComment = textarea.New()
	m.pipelineScreen.planComment.SetWidth(80)
	m.pipelineScreen.planComment.SetHeight(2)
	m.pipelineScreen.planComment.Focus()
	m.width = 120
	m.height = 40
	m.recalculateLayout()

	// Simulate EventChatResponse
	m.pipelineScreen.ApplyEvent(orchestrator.Event{
		Type:     orchestrator.EventChatResponse,
		ChatText: "Step 3 initializes the config parser.",
	}, m.width)

	// Verify state
	if m.pipelineScreen.content != ContentHumanGate {
		t.Errorf("expected ContentHumanGate, got %d", m.pipelineScreen.content)
	}
	if len(m.pipelineScreen.chatHistory) != 1 {
		t.Fatalf("expected 1 chat entry, got %d", len(m.pipelineScreen.chatHistory))
	}
	if m.pipelineScreen.chatHistory[0].Role != ChatRoleArchitect {
		t.Errorf("expected architect role, got %q", m.pipelineScreen.chatHistory[0].Role)
	}
	if !m.pipelineScreen.hasPlanComment {
		t.Error("expected comment textarea restored")
	}
	if m.pipelineScreen.finalPlan != "# Plan\n\n## Goal\nOriginal" {
		t.Error("expected plan unchanged after chat-only response")
	}

	// Verify render includes chat history
	view := m.pipelineScreen.viewPlanReview(100)
	if !strings.Contains(view, "Architect:") {
		t.Error("expected 'Architect:' prefix in rendered view")
	}
	if !strings.Contains(view, "config parser") {
		t.Error("expected chat text in rendered view")
	}
}

func TestTUI_PlanDiffToggle(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentHumanGate
	m.pipelineScreen.hasPlan = true
	planText := "# Plan\n\n## Goal\nNew."
	diffText := "--- a/plan.md\n+++ b/plan.md\n@@ -1,4 +1,4 @@\n # Plan\n \n ## Goal\n-Old.\n+New.\n"
	m.pipelineScreen.planDiff = diffText
	m.pipelineScreen.finalPlan = planText
	m.pipelineScreen.awaitingPlanDecision = true
	// Simulate what EventGateRequest would have done: PlanFrame with inlined diff
	m.pipelineScreen.frameList.AppendFrame(Frame{
		Kind:  PlanFrame,
		State: FrameInProgress,
		Parts: []ContentPart{{IsText: true, Text: planText + "\n── plan diff ──\n" + diffText}},
	})
	m.pipelineScreen.planFrameIdx = 0
	m.pipelineScreen.planDiffLineOffset = strings.Count(planText, "\n") + 2
	m.width = 120
	m.height = 40
	m.recalculateLayout()
	m.pipelineScreen.SyncViewports()

	// Ctrl+D no longer switches to ContentPlanDiff — it scrolls to the diff section
	result, _ := m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	model := result.(Model)
	if model.pipelineScreen.content != ContentHumanGate {
		t.Errorf("expected ContentHumanGate after Ctrl+D (no mode switch), got %d", model.pipelineScreen.content)
	}
}

func TestTUI_PlanDiffIgnoredWithoutHistory(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentHumanGate
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nTest"
	m.pipelineScreen.planDiff = "" // no history (initial plan, no revisions)
	m.width = 120
	m.height = 40
	m.recalculateLayout()

	result, _ := m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	model := result.(Model)
	// Should stay in plan review — no diff available
	if model.pipelineScreen.content != ContentHumanGate {
		t.Errorf("expected ContentHumanGate (no diff available), got %d", model.pipelineScreen.content)
	}
}

func TestTUI_ReviewTokenAccumulation(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.chatHistory = []ChatEntry{{Role: ChatRoleUser, Text: "q1"}}

	m.pipelineScreen.ApplyEvent(orchestrator.Event{
		Type:         orchestrator.EventAgentDone,
		AgentID:      "architect",
		InputTokens:  1000,
		OutputTokens: 500,
	}, m.width)

	if m.pipelineScreen.reviewTokensIn != 1000 {
		t.Errorf("reviewTokensIn = %d, want 1000", m.pipelineScreen.reviewTokensIn)
	}
	if m.pipelineScreen.reviewTokensOut != 500 {
		t.Errorf("reviewTokensOut = %d, want 500", m.pipelineScreen.reviewTokensOut)
	}
}

func TestTUI_ChatHistory_UserAndArchitect(t *testing.T) {
	t.Skip("skipped: ChatHistory flow replaced by HumanChatMode in v6")
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentHumanGate
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nTest"
	m.pipelineScreen.awaitingPlanDecision = true
	// channel removed — test checks state only
	m.pipelineScreen.hasPlanComment = true
	m.pipelineScreen.planComment = textarea.New()
	m.pipelineScreen.planComment.SetWidth(80)
	m.pipelineScreen.planComment.SetHeight(2)
	m.pipelineScreen.planComment.Focus()
	m.pipelineScreen.planComment.SetValue("why this approach?")
	m.width = 120
	m.height = 40
	m.recalculateLayout()

	// Press Enter to submit comment
	result, _ := sendKey(m, tea.KeyEnter)
	model := result.(Model)

	// Verify user's message was added to chat history
	if len(model.pipelineScreen.chatHistory) != 1 {
		t.Fatalf("expected 1 chat entry, got %d", len(model.pipelineScreen.chatHistory))
	}
	if model.pipelineScreen.chatHistory[0].Role != ChatRoleUser {
		t.Errorf("expected 'you' role, got %q", model.pipelineScreen.chatHistory[0].Role)
	}
	if model.pipelineScreen.chatHistory[0].Text != "why this approach?" {
		t.Errorf("expected 'why this approach?', got %q", model.pipelineScreen.chatHistory[0].Text)
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
