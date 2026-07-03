package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/mcp"
	"github.com/xiii/orqestra/internal/orchestrator"
	"github.com/xiii/orqestra/internal/tui/keymap"
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

// applyEvent drives one RunEvent through Model.Update as a real runEventMsg
// would arrive off handle.Events, returning the updated Model.
func applyEvent(m Model, ev orchestrator.RunEvent) Model {
	result, _ := m.Update(runEventMsg{ev: ev})
	return result.(Model)
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
	// Events + Intents must be set on the returned model (regression: evaluation-order bug).
	if model.events == nil {
		t.Error("model.events is nil after prompt submit — pipeline state will never be received")
	}
	if model.intents == nil {
		t.Error("model.intents is nil after prompt submit — gate/question responses will never be sent")
	}
	if model.cancelCause == nil {
		t.Error("model.cancelCause is nil after prompt submit — pipeline cannot be stopped")
	}
	// Cmd should be non-nil (waitForEvent + tick)
	if cmd == nil {
		t.Error("expected non-nil cmd from startPipeline")
	}
	// Clean up: cancel the pipeline
	model.cancelCause(nil)
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

	m = applyEvent(m, orchestrator.EventGateOpened{
		GateID: 1,
		Request: orchestrator.GateRequest{
			Position:          orchestrator.GateAfterDeliberation,
			FinalPlanMarkdown: "# Plan\n\n## Goal\nAdd feature X\n\n## Work Packages\n\n### 1. Step 1",
		},
	})

	if !m.pipelineScreen.awaitingPlanDecision {
		t.Error("expected the gate to open (awaitingPlanDecision)")
	}
	if !m.pipelineScreen.hasPlan {
		t.Error("expected hasPlan=true")
	}
}

// ^A at a plan gate approves it (a hard gate over the always-focused chat):
// the gate closes and an ApprovePlanIntent is queued.
func TestGate_ApproveClosesGateAndEmitsIntent(t *testing.T) {
	s := NewPipelineScreen("test", runeUI{}, keymap.Default())
	s.awaitingPlanDecision = true
	s.hasPlan = true
	s.finalPlan = "# Plan\n\n## Goal\nTest"

	s, _ = s.Update(tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})

	if s.awaitingPlanDecision {
		t.Error("expected the gate closed after ^A")
	}
	if _, ok := s.PendingIntent.(ApprovePlanIntent); !ok {
		t.Fatalf("expected ApprovePlanIntent, got %T", s.PendingIntent)
	}
}

// Typing a reply at a plan gate revises it (a soft gate): the gate closes and a
// CommentPlanIntent carries the real typed text — not the old "user comment" stub.
func TestGate_ReplyRevisesWithRealComment(t *testing.T) {
	s := NewPipelineScreen("test", runeUI{}, keymap.Default())
	s.awaitingPlanDecision = true
	s.hasPlan = true
	s.finalPlan = "# Plan"
	s.chat.input.SetValue("tighten the error handling")

	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	intent, ok := s.PendingIntent.(CommentPlanIntent)
	if !ok {
		t.Fatalf("expected CommentPlanIntent, got %T", s.PendingIntent)
	}
	if intent.Comment != "tighten the error handling" {
		t.Errorf("comment = %q, want the typed text", intent.Comment)
	}
	if s.awaitingPlanDecision {
		t.Error("expected the gate closed after a reply")
	}
}

// ^E on the plan gate writes the plan to a temp file and asks the app to open
// $EDITOR; the gate stays open while editing happens out-of-band (D8).
func TestTUI_PlanEditOpensExternalEditor(t *testing.T) {
	const plan = "# Plan\n\n## Goal\nOriginal"
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.awaitingPlanDecision = true
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = plan

	result, _ := sendCtrl(m, 'e')
	model := result.(Model)

	if !model.pipelineScreen.awaitingPlanDecision {
		t.Error("expected the gate to stay open (awaitingPlanDecision) after ^E")
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
	m.cancelCause = func(error) { cancelled = true }

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
	// testModel() does not wire an Intents channel, so processIntent's
	// sendIntent Cmd is a no-op when invoked (nil-guarded). The bridge
	// round-trip assertion lives in the orchestrator/mcp packages' own tests.
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
	m.cancelCause = func(error) { cancelled = true }

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

	// Agent appears as running.
	m = applyEvent(m, orchestrator.EventAgentStarted{AgentID: "researcher"})

	if len(m.pipelineScreen.agents) != 1 || m.pipelineScreen.agents[0].State != AgentStateRunning {
		t.Errorf("expected 1 agent running, got %+v", m.pipelineScreen.agents)
	}

	// Agent transitions to done.
	m = applyEvent(m, orchestrator.EventAgentDone{AgentID: "researcher"})

	if m.pipelineScreen.agents[0].State != AgentStateDone {
		t.Errorf("expected agent state 'done', got %q", m.pipelineScreen.agents[0].State)
	}
}

func TestTUI_DoubleCtrlC(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.pipelineScreen.active = true
	m.cancelCause = func(error) {}

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
	m.cancelCause = func(error) {}

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

	m = applyEvent(m, orchestrator.EventRunFinished{
		Result: orchestrator.Result{WorkerValidation: "✓ tests pass\n✓ build succeeds"},
	})

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

	// tickMsg should not schedule another tick when not in pipeline state
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
	m.pipelineScreen.timeline.SetRect(m.regions.timeline)

	m = applyEvent(m, orchestrator.EventAgentStarted{AgentID: "researcher"})
	m = applyEvent(m, orchestrator.EventDelta{AgentID: "researcher", Text: "Partial in progress"})

	m.recalculateLayout()

	view := viewString(m)
	if !strings.Contains(view, "Partial in progress") {
		t.Error("expected streaming delta to appear in view (TurnGroup brief and console)")
	}
	// The active TurnGroup is the timeline tail — timeline has content.
	if !m.pipelineScreen.timeline.HasContent() {
		t.Error("expected timeline to have content (active TurnGroup tail)")
	}
	if m.pipelineScreen.currentTurn == nil {
		t.Error("expected active TurnGroup after streaming")
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
	model2.cancelCause(nil)
}

func TestTUI_PlanGateBlocksOverwrite(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming

	m = applyEvent(m, orchestrator.EventGateOpened{
		GateID: 1,
		Request: orchestrator.GateRequest{
			Position:          orchestrator.GateAfterDeliberation,
			FinalPlanMarkdown: "# Plan\n\n## Goal\nTest",
		},
	})

	if !m.pipelineScreen.awaitingPlanDecision {
		t.Fatalf("expected ContentHumanGate, got %d", m.pipelineScreen.content)
	}

	// An unrelated event (e.g. a phase transition) arriving while the gate is
	// open must never clobber the open gate.
	m = applyEvent(m, orchestrator.EventPhaseStarted{Phase: orchestrator.PhaseExecuting})

	if !m.pipelineScreen.awaitingPlanDecision {
		t.Errorf("gate was overwritten by an unrelated event: content=%d", m.pipelineScreen.content)
	}
}

func TestTUI_EditorReturn(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.awaitingPlanDecision = true
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

// TestTUI_EventLoopPlanGate exercises the event-driven path end to end: a
// runEventMsg carrying EventAgentStarted then EventGateOpened arrives, and
// Update leaves the model awaiting the plan decision with a re-armed
// waitForEvent command.
func TestTUI_EventLoopPlanGate(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.pipelineScreen.active = true
	m.pipelineScreen.agents = []AgentRow{{ID: "architect", State: AgentStateRunning, StartedAt: time.Now()}}

	events := make(chan orchestrator.RunEvent, 4)
	m.events = events

	planMD := "# Plan\n\n## Goal\nAdd X.\n\n## Work Packages\n\n### 1. Step 1"

	result, cmd := m.Update(runEventMsg{ev: orchestrator.EventGateOpened{
		GateID: 1,
		Request: orchestrator.GateRequest{
			Position:          orchestrator.GateAfterDeliberation,
			FinalPlanMarkdown: planMD,
		},
	}})
	model := result.(Model)

	if !model.pipelineScreen.awaitingPlanDecision {
		t.Errorf("expected the gate to open, got content=%d", model.pipelineScreen.content)
	}
	if !model.pipelineScreen.hasPlan {
		t.Error("expected hasPlan=true")
	}
	if model.pipelineScreen.finalPlan != planMD {
		t.Errorf("expected finalPlan to be set, got %q", model.pipelineScreen.finalPlan)
	}
	// cmd should be non-nil: waitForEvent re-arms since this was not RunFinished.
	if cmd == nil {
		t.Error("expected non-nil cmd (waitForEvent re-arm)")
	}
}

// TestTUI_RunFinishedDoesNotOverwriteGate verifies that when the pipeline
// finishes while awaitingPlanDecision, onRunFinished forces the gate closed
// (defensive: EventRunFinished is only ever emitted after any real gate
// loop concluded, but the TUI must never get stuck awaiting a decision no
// one will send).
func TestTUI_RunFinishedDoesNotOverwriteGate(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.active = true

	m = applyEvent(m, orchestrator.EventGateOpened{
		GateID: 1,
		Request: orchestrator.GateRequest{
			Position:          orchestrator.GateAfterDeliberation,
			FinalPlanMarkdown: "# Plan\n\n## Goal\nTest",
		},
	})

	if !m.pipelineScreen.awaitingPlanDecision {
		t.Fatalf("expected the gate open, got content=%d", m.pipelineScreen.content)
	}

	m = applyEvent(m, orchestrator.EventRunFinished{Result: orchestrator.Result{Status: orchestrator.StatusFailed}})

	if m.pipelineScreen.awaitingPlanDecision {
		t.Error("expected onRunFinished to force the gate closed")
	}
	if m.pipelineScreen.content != ContentCompletion {
		t.Errorf("expected ContentCompletion, got %d", m.pipelineScreen.content)
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
	model3.cancelCause(nil)
}

// TestTUI_FinalDeltaRenderedBeforeRunFinished is the J30 gate proof: the
// emitter guarantees FIFO ordering (a Delta emitted before EventRunFinished
// is always delivered before it — emitter.go), so by the time the TUI
// applies EventRunFinished, an EventDelta from the run's final turn must
// already be reflected in the pipeline screen's accumulated state — never
// dropped or "arriving too late" to render. This drives the REAL
// waitForEvent → runEventMsg → Update → re-arm loop (not just a direct
// ApplyEvent call) over a real buffered channel, with the same event order
// a real run produces (AgentDone always precedes RunFinished — RunPipeline
// only reaches Finished after its last agent's Done/Failed).
func TestTUI_FinalDeltaRenderedBeforeRunFinished(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.pipelineScreen.active = true
	_ = m.pipelineScreen.Start("final delta test")
	m.recalculateLayout()

	events := make(chan orchestrator.RunEvent, 4)
	events <- orchestrator.EventAgentStarted{AgentID: "worker"}
	events <- orchestrator.EventDelta{AgentID: "worker", Text: "final words before done"}
	events <- orchestrator.EventAgentDone{AgentID: "worker"}
	events <- orchestrator.EventRunFinished{Result: orchestrator.Result{Status: orchestrator.StatusSuccess}}
	m.events = events

	// Drive the loop exactly as bubbletea would: invoke the Cmd, feed its Msg
	// back into Update, repeat until Update stops returning a Cmd (RunFinished).
	var cmd tea.Cmd = waitForEvent(m.events)
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			break
		}
		var result tea.Model
		result, cmd = m.Update(msg)
		m = result.(Model)
	}

	if m.pipelineScreen.content != ContentCompletion {
		t.Fatalf("expected ContentCompletion after RunFinished, got %d", m.pipelineScreen.content)
	}
	// The delta must have reached the timeline (accumulated into the turn
	// that was sealed+promoted before the done-summary was appended) — never
	// silently dropped or arriving "too late" to render.
	if !strings.Contains(m.pipelineScreen.timeline.View(), "final words before done") {
		t.Errorf("expected the final delta text in the timeline, got:\n%s", m.pipelineScreen.timeline.View())
	}
}

// TestApplyEvent_TerminalErrShowsInCompletion verifies that a pipeline
// failure reported via EventRunFinished is visible in the completion screen
// — the real path through runEventMsg → ApplyEvent.
func TestApplyEvent_TerminalErrShowsInCompletion(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.active = true

	runErr := errors.New("research: read plan: model session x completed but did not write a plan file")
	m = applyEvent(m, orchestrator.EventRunFinished{Result: orchestrator.Result{Status: orchestrator.StatusFailed}, Err: runErr})

	if m.pipelineScreen.lastErr == nil {
		t.Fatal("lastErr is nil after failed pipeline; ApplyEvent must copy EventRunFinished.Err")
	}
	out := m.pipelineScreen.viewCompletion(80)
	if !strings.Contains(out, "Error:") {
		t.Errorf("viewCompletion missing 'Error:' line:\n%s", out)
	}
	if !strings.Contains(m.pipelineScreen.lastErr.Error(), "did not write a plan file") {
		t.Errorf("unexpected lastErr content: %v", m.pipelineScreen.lastErr)
	}
}

// TestApplyEvent_ConflictFilesShowInCompletion is the WP3/J10 TUI gate: when
// Result.ConflictFiles is non-empty (integrator gave up on a merge conflict),
// the done screen must render the conflict file list — never hide it behind a
// bare completion event.
func TestApplyEvent_ConflictFilesShowInCompletion(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.active = true

	conflictFiles := []string{"internal/foo.go", "internal/bar.go"}
	m = applyEvent(m, orchestrator.EventRunFinished{Result: orchestrator.Result{Status: orchestrator.StatusFailed, ConflictFiles: conflictFiles}})

	out := m.pipelineScreen.viewCompletion(80)
	for _, f := range conflictFiles {
		if !strings.Contains(out, f) {
			t.Errorf("viewCompletion missing conflict file %q:\n%s", f, out)
		}
	}
	if !strings.Contains(strings.ToLower(out), "conflict") {
		t.Errorf("viewCompletion does not mention the merge conflict:\n%s", out)
	}
}

// TestApplyEvent_ValidationVerdictShowsInCompletion is the WP8/J33 TUI
// gate: when Result.ValidationVerdict is FAIL (worker self-reported failure),
// the done screen must render it — never leave a failed self-validation
// invisible next to the completion summary.
func TestApplyEvent_ValidationVerdictShowsInCompletion(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.active = true

	// Deliberately no WorkerValidation raw text here: this isolates the check to
	// the parsed verdict label itself, not incidental substring overlap with raw
	// validation prose (e.g. "...tests failed" would contain "FAIL" too).
	m = applyEvent(m, orchestrator.EventRunFinished{Result: orchestrator.Result{
		Status:            orchestrator.StatusSuccess,
		ValidationVerdict: agent.VerdictFail,
	}})

	out := m.pipelineScreen.viewCompletion(80)
	if !strings.Contains(strings.ToUpper(out), "FAIL") {
		t.Errorf("viewCompletion does not render the FAIL validation verdict:\n%s", out)
	}
}

// TestApplyEvent_ValidationVerdictUnknownWhenNotRun proves the done screen
// never claims a verdict it doesn't have: when validation never ran (empty
// Result.ValidationVerdict) but other completion state exists, it renders
// UNKNOWN rather than defaulting to something that looks like a pass.
func TestApplyEvent_ValidationVerdictUnknownWhenNotRun(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.active = true

	m = applyEvent(m, orchestrator.EventRunFinished{Result: orchestrator.Result{
		Status:           orchestrator.StatusSuccess,
		WorkerValidation: "some raw text but no parsed verdict",
	}})

	out := m.pipelineScreen.viewCompletion(80)
	if !strings.Contains(strings.ToUpper(out), "UNKNOWN") {
		t.Errorf("viewCompletion does not render UNKNOWN for an absent validation verdict:\n%s", out)
	}
}

// TestApplyEvent_AgentFailedErrShowsInCompletion verifies that an agent
// failure error carried on EventAgentFailed reaches s.lastErr via ApplyEvent.
func TestApplyEvent_AgentFailedErrShowsInCompletion(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.active = true

	agentErr := errors.New("research: read plan: model did not write a plan file")
	m = applyEvent(m, orchestrator.EventAgentStarted{AgentID: "researcher", Meta: orchestrator.AgentMeta{ModelRef: "test"}})
	m = applyEvent(m, orchestrator.EventAgentFailed{AgentID: "researcher", Err: agentErr})

	if m.pipelineScreen.lastErr == nil {
		t.Fatal("lastErr is nil after agent failure; EventAgentFailed.Err must propagate through ApplyEvent")
	}
	if !strings.Contains(m.pipelineScreen.lastErr.Error(), "did not write a plan file") {
		t.Errorf("unexpected lastErr: %v", m.pipelineScreen.lastErr)
	}
}
