package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// noopRunner is a no-op CLIRunner for tests that don't need real LLM calls.
type noopRunner struct{}

func (n *noopRunner) RunPrint(_ context.Context, _, _ string) (harness.RunResult, error) {
	return harness.RunResult{Output: `{"verdict":"accept","brief":{"task":"t","end_state":"e","scope":[],"non_scope":[]},"questions":[],"confidence":0.9}`}, nil
}

func (n *noopRunner) RunStreaming(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	return harness.RunResult{Output: `{"verdict":"accept","brief":{"task":"t","end_state":"e","scope":[],"non_scope":[]},"questions":[],"confidence":0.9}`}, nil
}

func (n *noopRunner) RunContinue(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	return harness.RunResult{Output: "✅ all pass"}, nil
}

// testModel creates a Model suitable for testing with a minimal mock engine.
func testModel() Model {
	engine := &orchestrator.Engine{
		Config: testConfig(),
		Runners: orchestrator.Runners{
			Gateway:    &noopRunner{},
			Researcher: &noopRunner{},
			Architect:  &noopRunner{},
			Worker:     &noopRunner{},
		},
	}
	m := NewModel(engine, "test.yaml")
	m.width = 120
	m.height = 40
	m.recalculateLayout()
	return m
}

func testConfig() *config.Config {
	return &config.Config{
		Gateway:    config.GatewayConfig{SystemPrompt: "test"},
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
	// Pipeline channels must be set on the returned model (regression: evaluation-order bug).
	if model.events == nil {
		t.Error("model.events is nil after prompt submit — pipeline events will never be received")
	}
	if model.pipelineScreen.streamBuf == nil {
		t.Error("model.pipelineScreen.streamBuf is nil after prompt submit — streaming output will not display")
	}
	if model.decisions == nil {
		t.Error("model.decisions is nil after prompt submit — gate responses will never be sent")
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

func TestTUI_PromptSkipGateway(t *testing.T) {
	m := testModel()
	m.promptScreen.SetValue("add a feature")

	result, cmd := sendCtrl(m, 's')
	model := result.(Model)

	if model.state != StatePipeline {
		t.Errorf("expected StatePipeline, got %d", model.state)
	}
	if model.pipelineScreen.goal != "add a feature" {
		t.Errorf("expected goal 'add a feature', got %q", model.pipelineScreen.goal)
	}
	// Same evaluation-order regression guard as TestTUI_PromptSubmit.
	if model.events == nil {
		t.Error("model.events is nil after skip-gateway submit")
	}
	if model.pipelineScreen.streamBuf == nil {
		t.Error("model.pipelineScreen.streamBuf is nil after skip-gateway submit")
	}
	if model.cancel == nil {
		t.Error("model.cancel is nil after skip-gateway submit")
	}
	if cmd == nil {
		t.Error("expected non-nil cmd from startPipeline")
	}
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

func TestTUI_CoachingRender(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.events = make(chan orchestrator.Event, 1) // need a channel for waitForEvent

	// Simulate receiving a coaching gate event
	event := orchestrator.Event{
		Type: orchestrator.EventGateRequest,
		Gate: orchestrator.GateRequest{
			Type: orchestrator.GateGatewayCoach,
			GatewayResult: agent.GatewayResult{
				Verdict: agent.GatewayVerdictCoach,
				Brief:   agent.PromptBrief{Task: "Improve auth module"},
				Questions: []agent.Question{
					{Text: "Which part?", Options: []string{"login", "signup"}, Default: "login"},
				},
			},
		},
	}

	m.pipelineScreen.ApplyEvent(event, m.width)

	if m.pipelineScreen.content != ContentCoaching {
		t.Errorf("expected ContentCoaching, got %d", m.pipelineScreen.content)
	}
	if len(m.pipelineScreen.answerFields) != 1 {
		t.Fatalf("expected 1 answer field, got %d", len(m.pipelineScreen.answerFields))
	}
	if m.pipelineScreen.answerFields[0].Value() != "login" {
		t.Errorf("expected default 'login', got %q", m.pipelineScreen.answerFields[0].Value())
	}
}

func TestTUI_CoachingSubmit(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentCoaching
	decisions := make(chan orchestrator.Decision, 1)
	m.decisions = decisions
	m.pipelineScreen.gatewayResult = agent.GatewayResult{
		Questions: []agent.Question{
			{Text: "Which part?", Options: []string{"a", "b"}, Default: "a"},
		},
	}

	// Create answer field with value
	m.pipelineScreen.answerFields = makeAnswerFields(m.pipelineScreen.gatewayResult.Questions, m.effectiveWidth())
	m.pipelineScreen.answerFields[0].SetValue("module b")
	m.pipelineScreen.answerCursor = 0

	// Press Enter to submit
	result, _ := sendKey(m, tea.KeyEnter)
	model := result.(Model)

	if model.pipelineScreen.content != ContentStreaming {
		t.Errorf("expected ContentStreaming after submit, got %d", model.pipelineScreen.content)
	}

	// Check decision was sent
	select {
	case d := <-decisions:
		if d.Type != orchestrator.DecisionApprove {
			t.Errorf("expected DecisionApprove, got %d", d.Type)
		}
		if len(d.GatewayAnswers) != 1 || d.GatewayAnswers[0].Answer != "module b" {
			t.Errorf("unexpected answers: %+v", d.GatewayAnswers)
		}
	default:
		t.Error("expected decision to be sent")
	}
}

func TestTUI_CoachingSkip(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentCoaching
	decisions := make(chan orchestrator.Decision, 1)
	m.decisions = decisions
	m.pipelineScreen.gatewayResult = agent.GatewayResult{
		Questions: []agent.Question{
			{Text: "Q1?", Options: []string{"a"}, Default: "a"},
		},
	}
	m.pipelineScreen.answerFields = makeAnswerFields(m.pipelineScreen.gatewayResult.Questions, m.effectiveWidth())
	m.pipelineScreen.answerCursor = 0

	result, _ := sendCtrl(m, 's')
	model := result.(Model)

	if model.pipelineScreen.content != ContentStreaming {
		t.Errorf("expected ContentStreaming after skip, got %d", model.pipelineScreen.content)
	}

	select {
	case d := <-decisions:
		if d.Type != orchestrator.DecisionSkip {
			t.Errorf("expected DecisionSkip, got %d", d.Type)
		}
	default:
		t.Error("expected skip decision")
	}
}

func TestTUI_PlanApproval(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.events = make(chan orchestrator.Event, 1)

	event := orchestrator.Event{
		Type: orchestrator.EventGateRequest,
		Gate: orchestrator.GateRequest{
			Type:              orchestrator.GatePlanApproval,
			FinalPlanMarkdown: "# Plan\n\n## Goal\nAdd feature X\n\n## Work Packages\n\n### 1. Step 1",
		},
	}

	m.pipelineScreen.ApplyEvent(event, m.width)

	if m.pipelineScreen.content != ContentPlanReview {
		t.Errorf("expected ContentPlanReview, got %d", m.pipelineScreen.content)
	}
	if !m.pipelineScreen.hasPlan {
		t.Error("expected hasPlan=true")
	}
}

func TestTUI_PlanApprove(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentPlanReview
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nTest"
	decisions := make(chan orchestrator.Decision, 1)
	m.decisions = decisions

	result, _ := sendRune(m, "a")
	model := result.(Model)

	if model.pipelineScreen.content != ContentStreaming {
		t.Errorf("expected ContentStreaming after approve, got %d", model.pipelineScreen.content)
	}

	select {
	case d := <-decisions:
		if d.Type != orchestrator.DecisionApprove {
			t.Errorf("expected DecisionApprove, got %d", d.Type)
		}
	default:
		t.Error("expected approve decision")
	}
}

func TestTUI_PlanEditSave(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentPlanReview
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nOriginal"
	decisions := make(chan orchestrator.Decision, 1)
	m.decisions = decisions

	// Press E to enter edit mode
	result, _ := sendRune(m, "e")
	model := result.(Model)

	if model.pipelineScreen.content != ContentPlanEdit {
		t.Fatalf("expected ContentPlanEdit, got %d", model.pipelineScreen.content)
	}
	if !model.pipelineScreen.hasPlanEditor {
		t.Fatal("expected hasPlanEditor=true")
	}

	// Simulate editing the content
	model.pipelineScreen.planEditor.SetValue(`{"goal":"Modified","steps":["s1"],"acceptance":["a1"]}`)

	// Press Ctrl+S to save
	result2, _ := sendCtrl(model, 's')
	model2 := result2.(Model)

	if model2.pipelineScreen.content != ContentStreaming {
		t.Errorf("expected ContentStreaming after save, got %d", model2.pipelineScreen.content)
	}

	select {
	case d := <-decisions:
		if d.Type != orchestrator.DecisionEdit {
			t.Errorf("expected DecisionEdit, got %d", d.Type)
		}
		if !strings.Contains(d.EditedContent, "Modified") {
			t.Errorf("expected edited content to contain 'Modified', got %q", d.EditedContent)
		}
	default:
		t.Error("expected edit decision")
	}
}

func TestTUI_PlanEditDiscard(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentPlanReview
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nOriginal"

	// Press E to enter edit mode
	result, _ := sendRune(m, "e")
	model := result.(Model)

	if model.pipelineScreen.content != ContentPlanEdit {
		t.Fatal("expected ContentPlanEdit")
	}

	// Press Esc to discard
	result2, _ := sendKey(model, tea.KeyEscape)
	model2 := result2.(Model)

	if model2.pipelineScreen.content != ContentPlanReview {
		t.Errorf("expected ContentPlanReview after discard, got %d", model2.pipelineScreen.content)
	}
}

func TestTUI_CancelAgent(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	cancelled := false
	m.cancel = func() { cancelled = true }

	result, _ := sendRune(m, "s")
	_ = result.(Model)

	if !cancelled {
		t.Error("expected cancel func to be called")
	}
}

func TestTUI_AgentNavigation(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.pipelineScreen.agents = []AgentRow{
		{ID: "gateway", State: "done"},
		{ID: "architect", State: "running"},
	}

	// Press 1 to view gateway history
	result, _ := sendRune(m, "1")
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
	m.pipelineScreen.agents = []AgentRow{{ID: "gateway", State: "done"}}

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

	result, _ := sendRune(m, "n")
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

	// Press N during active pipeline — should show confirmation
	result, _ := sendRune(m, "n")
	model := result.(Model)

	if model.state != StatePipeline {
		t.Error("expected to stay in StatePipeline until confirmed")
	}
	if !model.pipelineScreen.confirmNew {
		t.Error("expected confirmNew=true")
	}

	// View should show confirmation message
	view := viewString(model)
	if !strings.Contains(view, "Pipeline is active") {
		t.Error("expected confirmation message in view")
	}

	// Press Y to confirm
	result2, _ := sendRune(model, "y")
	model2 := result2.(Model)

	if model2.state != StatePrompt {
		t.Errorf("expected StatePrompt after Y confirm, got %d", model2.state)
	}
	if !cancelled {
		t.Error("expected cancel to be called")
	}
	if !strings.Contains(model2.promptScreen.Value(), "active task") {
		t.Errorf("expected prompt pre-filled with goal, got %q", model2.promptScreen.Value())
	}
}

func TestTUI_SidebarUpdates(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.events = make(chan orchestrator.Event, 1)

	// AgentStarted
	m.pipelineScreen.ApplyEvent(orchestrator.Event{
		Type:    orchestrator.EventAgentStarted,
		AgentID: "gateway",
	}, m.width)

	if len(m.pipelineScreen.agents) != 1 || m.pipelineScreen.agents[0].State != "running" {
		t.Errorf("expected 1 agent running, got %+v", m.pipelineScreen.agents)
	}

	// AgentDone
	m.pipelineScreen.ApplyEvent(orchestrator.Event{
		Type:    orchestrator.EventAgentDone,
		AgentID: "gateway",
	}, m.width)

	if m.pipelineScreen.agents[0].State != "done" {
		t.Errorf("expected agent state 'done', got %q", m.pipelineScreen.agents[0].State)
	}
}

func TestTUI_FullDashboard(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.pipelineScreen.agents = []AgentRow{
		{ID: "gateway", State: "done"},
		{ID: "architect", State: "running"},
	}

	// Press D to toggle dashboard
	result, _ := sendRune(m, "d")
	model := result.(Model)

	if !model.pipelineScreen.showDashboard {
		t.Error("expected showDashboard=true")
	}

	// View should contain dashboard content
	view := viewString(model)
	if !strings.Contains(view, "gateway") || !strings.Contains(view, "architect") {
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

	// First Ctrl+C
	result, cmd := sendCtrl(m, 'c')
	model := result.(Model)
	if cmd != nil {
		t.Error("first Ctrl+C should not quit")
	}
	if model.ctrlC != 1 {
		t.Errorf("expected ctrlC=1, got %d", model.ctrlC)
	}

	// Second Ctrl+C
	_, cmd = sendCtrl(model, 'c')
	if cmd == nil {
		t.Error("second Ctrl+C should trigger quit")
	}
}

func TestTUI_CompletionValidation(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.events = make(chan orchestrator.Event, 1)

	event := orchestrator.Event{
		Type:             orchestrator.EventComplete,
		WorkerValidation: "✅ tests pass\n✅ build succeeds",
	}

	m.pipelineScreen.ApplyEvent(event, m.width)

	if m.pipelineScreen.content != ContentCompletion {
		t.Errorf("expected ContentCompletion, got %d", m.pipelineScreen.content)
	}
	if !m.pipelineScreen.hasValidation {
		t.Error("expected hasValidation=true")
	}
}

// makeAnswerFields creates textarea answer fields from questions (helper for tests).
func makeAnswerFields(questions []agent.Question, width int) []textarea.Model {
	fields := make([]textarea.Model, len(questions))
	for i, q := range questions {
		ta := textarea.New()
		ta.SetWidth(width - 10)
		ta.SetHeight(1)
		ta.CharLimit = 512
		if q.Default != "" {
			ta.SetValue(q.Default)
		}
		if i == 0 {
			ta.Focus()
		}
		fields[i] = ta
	}
	return fields
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
	m.width = 120
	m.height = 40
	m.pipelineScreen.agents = []AgentRow{
		{ID: "gateway", State: "done", Elapsed: 3 * time.Second, InputTokens: 1218, OutputTokens: 402},
		{ID: "architect", State: "running", StartedAt: time.Now().Add(-24 * time.Second), InputTokens: 0, OutputTokens: 0},
	}
	m.recalculateLayout()
	m.pipelineScreen.SyncViewports()

	view := viewString(m)

	if !strings.Contains(view, "1.6k") {
		t.Error("expected sidebar to show formatted token count '1.6k' for gateway")
	}
	if !strings.Contains(view, "total:") {
		t.Error("expected sidebar to show totals row")
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
		{ID: "gateway", State: "done", Elapsed: 3 * time.Second, InputTokens: 1218, OutputTokens: 402},
	}
	m.recalculateLayout()
	m.pipelineScreen.SyncViewports()

	view := viewString(m)
	if !strings.Contains(view, "In Tok") {
		t.Error("expected dashboard header with 'In Tok'")
	}
	if !strings.Contains(view, "1,218") {
		t.Error("expected dashboard to show input tokens '1,218'")
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
	m.events = make(chan orchestrator.Event, 5)

	// Create a shared stream buffer (like the orchestrator would)
	stream := orchestrator.NewStreamBuffer(200)
	m.pipelineScreen.streamBuf = stream

	// Simulate agent start + streaming output via the shared buffer
	stream.SetAgent("gateway")
	stream.Append("Analyzing prompt...\nProcessing request...")

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
	if !strings.Contains(view, "gateway") {
		t.Error("expected agent name in streaming view")
	}
}

func TestTUI_StreamingOutputReset(t *testing.T) {
	stream := orchestrator.NewStreamBuffer(200)

	// Simulate first agent
	stream.SetAgent("gateway")
	stream.Append("gateway output line")

	agentID, lines, _ := stream.Snapshot()
	if agentID != "gateway" {
		t.Errorf("expected agent 'gateway', got %q", agentID)
	}
	if len(lines) == 0 {
		t.Fatal("expected stream lines from gateway")
	}

	// Simulate second agent — buffer should reset
	stream.SetAgent("architect")

	agentID2, lines2, _ := stream.Snapshot()
	if agentID2 != "architect" {
		t.Errorf("expected agent 'planner', got %q", agentID2)
	}
	if len(lines2) != 0 {
		t.Errorf("expected stream buffer cleared on new agent, got %d lines", len(lines2))
	}
}

func TestStreamBuffer_TokenAccumulation(t *testing.T) {
	stream := orchestrator.NewStreamBuffer(200)
	stream.SetAgent("gateway")

	// Simulate token-level writes (each content_block_delta is a few chars)
	stream.Append("I")
	stream.Append("'ll")
	stream.Append(" analyze")
	stream.Append(" the")
	stream.Append(" request")

	_, lines, _ := stream.Snapshot()
	if len(lines) != 1 {
		t.Errorf("expected 1 line from token-level writes, got %d: %v", len(lines), lines)
	}
	if lines[0] != "I'll analyze the request" {
		t.Errorf("unexpected accumulated line: %q", lines[0])
	}

	// Now write a newline to start a new line
	stream.Append(".\nNext line here")

	_, lines, _ = stream.Snapshot()
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
		{ID: "gateway", State: "done"},
		{ID: "architect", State: "failed"},
	}
	m.pipelineScreen.lastErr = fmt.Errorf("planner failed")
	m.pipelineScreen.finalPlan = "# Old Plan"
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.workerValidation = "old validation"
	m.pipelineScreen.hasValidation = true

	// Press N to start new run
	result, _ := sendRune(m, "n")
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
	m.pipelineScreen.agents = []AgentRow{{ID: "gateway", State: "done"}}
	m.pipelineScreen.lastErr = fmt.Errorf("old error")

	// Press N, then submit new prompt via Enter
	result, _ := sendRune(m, "n")
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
	m.events = make(chan orchestrator.Event, 1)

	// Simulate gate event
	m.pipelineScreen.ApplyEvent(orchestrator.Event{
		Type: orchestrator.EventGateRequest,
		Gate: orchestrator.GateRequest{
			Type:              orchestrator.GatePlanApproval,
			FinalPlanMarkdown: "# Plan\n\n## Goal\nTest",
		},
	}, m.width)

	if m.pipelineScreen.content != ContentPlanReview {
		t.Fatalf("expected ContentPlanReview, got %d", m.pipelineScreen.content)
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
	if m.pipelineScreen.content != ContentPlanReview {
		t.Errorf("gate was overwritten by stale EventPhaseChange: content=%d", m.pipelineScreen.content)
	}
	// Phase should not be updated while gate is active
	if m.pipelineScreen.phase == orchestrator.PhaseExecuting {
		t.Error("phase was updated despite awaitingPlanDecision being true")
	}
}

func TestTUI_PlanReviewComment(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentPlanReview
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nTest"
	decisions := make(chan orchestrator.Decision, 1)
	m.decisions = decisions
	m.events = make(chan orchestrator.Event, 1)

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

	select {
	case d := <-decisions:
		if d.Type != orchestrator.DecisionComment {
			t.Errorf("expected DecisionComment, got %d", d.Type)
		}
		if d.Comment != "please add error handling" {
			t.Errorf("expected comment text, got %q", d.Comment)
		}
	default:
		t.Error("expected comment decision to be sent")
	}
}

func TestTUI_PlanReviewExternalEditor(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentPlanReview
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nTest"
	m.pipelineScreen.planFilePath = "/tmp/test-plan.md"
	m.pipelineScreen.hasPlanComment = true
	m.pipelineScreen.planComment = textarea.New()

	// Press Ctrl+E to open external editor
	result, cmd := sendCtrl(m, 'e')
	model := result.(Model)

	if !model.pipelineScreen.editorRunning {
		t.Error("expected editorRunning=true after Ctrl+E")
	}
	if cmd == nil {
		t.Error("expected non-nil cmd (ExecProcess)")
	}
}

func TestTUI_PlanReviewGlamour(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentPlanReview
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
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentPlanReview
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = "# Plan\n\nOriginal content"
	decisions := make(chan orchestrator.Decision, 1)
	m.decisions = decisions
	m.events = make(chan orchestrator.Event, 1)

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

	// Simulate editor return
	result, _ := m.Update(editorReturnMsg{err: nil})
	model := result.(Model)

	if model.pipelineScreen.finalPlan != modifiedPlan {
		t.Errorf("expected updated finalPlan, got %q", model.pipelineScreen.finalPlan)
	}
	if model.pipelineScreen.content != ContentStreaming {
		t.Errorf("expected ContentStreaming after editor return with changes, got %d", model.pipelineScreen.content)
	}

	select {
	case d := <-decisions:
		if d.Type != orchestrator.DecisionEdit {
			t.Errorf("expected DecisionEdit, got %d", d.Type)
		}
		if d.EditedContent != modifiedPlan {
			t.Errorf("expected edited content to match modified plan")
		}
	default:
		t.Error("expected edit decision to be sent")
	}
}

// TestTUI_DrainLoopPlanGate exercises the full Update drain loop:
// events for planner-done → plan-ready → gate-request arrive in a burst
// and must all be consumed, leaving the model in ContentPlanReview.
func TestTUI_DrainLoopPlanGate(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.pipelineScreen.agents = []AgentRow{{ID: "architect", State: "running", StartedAt: time.Now()}}

	events := make(chan orchestrator.Event, 16)
	m.events = events

	planMD := "# Plan\n\n## Goal\nAdd X.\n\n## Work Packages\n\n### 1. Step 1"

	// Buffer all three events before the TUI reads any of them.
	events <- orchestrator.Event{Type: orchestrator.EventAgentDone, AgentID: "architect", InputTokens: 100, OutputTokens: 50}
	events <- orchestrator.Event{Type: orchestrator.EventPlanReady, FinalPlan: planMD}
	events <- orchestrator.Event{Type: orchestrator.EventGateRequest, Gate: orchestrator.GateRequest{
		Type:              orchestrator.GatePlanApproval,
		FinalPlanMarkdown: planMD,
		PlanFilePath:      "/tmp/plan.md",
	}}

	// Simulate what waitForEvent would do: read the first event and wrap it.
	firstEvent := <-events
	result, cmd := m.Update(OrchestratorEventMsg{Event: firstEvent})
	model := result.(Model)

	if model.pipelineScreen.content != ContentPlanReview {
		t.Errorf("expected ContentPlanReview after drain, got %d", model.pipelineScreen.content)
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
	if !model.pipelineScreen.hasPlanComment {
		t.Error("expected hasPlanComment=true (comment textarea initialised)")
	}
	if cmd == nil {
		t.Error("expected non-nil cmd (waitForEvent)")
	}
}

// TestTUI_ChannelCloseDoesNotOverwriteGate verifies that when the events channel
// closes while awaitingPlanDecision, the content stays on ContentPlanReview.
func TestTUI_ChannelCloseDoesNotOverwriteGate(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentPlanReview
	m.pipelineScreen.awaitingPlanDecision = true
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nTest"

	// pipelineClosedMsg must NOT overwrite the gate.
	result, _ := m.Update(pipelineClosedMsg{})
	model := result.(Model)

	if model.pipelineScreen.content != ContentPlanReview {
		t.Errorf("pipelineClosedMsg overwrite gate: expected ContentPlanReview, got %d", model.pipelineScreen.content)
	}
}

// TestTUI_DrainLoopChannelCloseAfterGate verifies that if the channel closes
// right after the gate event is drained, the gate is preserved.
func TestTUI_DrainLoopChannelCloseAfterGate(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	m.pipelineScreen.agents = []AgentRow{{ID: "architect", State: "running", StartedAt: time.Now()}}

	events := make(chan orchestrator.Event, 16)
	m.events = events

	planMD := "# Plan\n\n## Goal\nX.\n\n## Work Packages\n\n### 1. Step"

	// Buffer gate event and close channel (simulating ctx cancel race).
	events <- orchestrator.Event{Type: orchestrator.EventGateRequest, Gate: orchestrator.GateRequest{
		Type:              orchestrator.GatePlanApproval,
		FinalPlanMarkdown: planMD,
	}}
	close(events)

	// Deliver the gate event via OrchestratorEventMsg. The drain loop will
	// read the closed channel on the next iteration.
	result, _ := m.Update(OrchestratorEventMsg{Event: <-events})

	// Channel was already closed, so the read above consumed the gate.
	// But we need to rebuild: put the gate event, close, then let drain run.
	// Redo with a fresh channel:
	events2 := make(chan orchestrator.Event, 16)
	m.events = events2
	events2 <- orchestrator.Event{Type: orchestrator.EventGateRequest, Gate: orchestrator.GateRequest{
		Type:              orchestrator.GatePlanApproval,
		FinalPlanMarkdown: planMD,
	}}
	close(events2)

	firstEvent := <-events2 // gate event
	result, _ = m.Update(OrchestratorEventMsg{Event: firstEvent})
	model := result.(Model)

	// After draining, the next read sees channel-closed.
	// awaitingPlanDecision should protect the gate.
	if model.pipelineScreen.content != ContentPlanReview {
		t.Errorf("expected ContentPlanReview (gate protected), got %d", model.pipelineScreen.content)
	}
}

// TestTUI_GlobalKeysBlockedInPlanReview ensures "d" and number keys
// are routed to the comment textarea, not to global handlers.
func TestTUI_GlobalKeysBlockedInPlanReview(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentPlanReview
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.hasPlanComment = true
	m.pipelineScreen.planComment = textarea.New()
	m.pipelineScreen.planComment.SetWidth(80)
	m.pipelineScreen.planComment.SetHeight(2)
	m.pipelineScreen.planComment.Focus()
	m.pipelineScreen.agents = []AgentRow{{ID: "architect", State: "done"}}

	// Press "d" — must NOT toggle dashboard
	result, _ := sendRune(m, "d")
	model := result.(Model)

	if model.pipelineScreen.showDashboard {
		t.Error("pressing 'd' in ContentPlanReview toggled dashboard instead of typing in comment textarea")
	}

	// Press "1" — must NOT switch to agent history
	result2, _ := sendRune(model, "1")
	model2 := result2.(Model)

	if model2.pipelineScreen.content != ContentPlanReview {
		t.Errorf("pressing '1' in ContentPlanReview switched content to %d", model2.pipelineScreen.content)
	}
}

// TestTUI_PlanReviewInputHeight verifies that the content height accounts
// for the taller input zone in ContentPlanReview mode.
func TestTUI_PlanReviewInputHeight(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentPlanReview
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
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentPlanReview
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
	if m.pipelineScreen.content != ContentPlanReview {
		t.Errorf("expected ContentPlanReview, got %d", m.pipelineScreen.content)
	}
	if len(m.pipelineScreen.chatHistory) != 1 {
		t.Fatalf("expected 1 chat entry, got %d", len(m.pipelineScreen.chatHistory))
	}
	if m.pipelineScreen.chatHistory[0].Role != "architect" {
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
	m.pipelineScreen.content = ContentPlanReview
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nNew"
	m.pipelineScreen.planDiff = "--- a/plan.md\n+++ b/plan.md\n@@ -1,4 +1,4 @@\n # Plan\n \n ## Goal\n-Old.\n+New.\n"
	m.pipelineScreen.awaitingPlanDecision = true
	m.width = 120
	m.height = 40
	m.recalculateLayout()

	// Press D to enter diff mode
	result, _ := m.Update(tea.KeyPressMsg{Code: 100, Text: "d"}) // 'd'
	model := result.(Model)
	if model.pipelineScreen.content != ContentPlanDiff {
		t.Errorf("expected ContentPlanDiff, got %d", model.pipelineScreen.content)
	}

	// Verify diff renders
	view := model.pipelineScreen.viewPlanDiff(100)
	if !strings.Contains(view, "Plan Diff") {
		t.Error("expected diff header in view")
	}

	// Press Esc to return
	result2, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model2 := result2.(Model)
	if model2.pipelineScreen.content != ContentPlanReview {
		t.Errorf("expected ContentPlanReview after Esc, got %d", model2.pipelineScreen.content)
	}
}

func TestTUI_PlanDiffIgnoredWithoutHistory(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentPlanReview
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nTest"
	m.pipelineScreen.planDiff = "" // no history (initial plan, no revisions)
	m.width = 120
	m.height = 40
	m.recalculateLayout()

	result, _ := m.Update(tea.KeyPressMsg{Code: 100, Text: "d"})
	model := result.(Model)
	// Should stay in plan review — no diff available
	if model.pipelineScreen.content != ContentPlanReview {
		t.Errorf("expected ContentPlanReview (no diff available), got %d", model.pipelineScreen.content)
	}
}

func TestTUI_ReviewTokenAccumulation(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.chatHistory = []ChatEntry{{Role: "you", Text: "q1"}}

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

func TestTUI_ViewPlanDiffViewport(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentPlanDiff
	m.pipelineScreen.planDiff = "diff --git a/plan.md b/plan.md\n# Plan\n-Old.\n+New.\n"
	m.pipelineScreen.diffViewport.SetWidth(100)
	m.pipelineScreen.diffViewport.SetHeight(20)
	m.pipelineScreen.diffViewport.SetContent(m.pipelineScreen.planDiff)
	m.width = 120
	m.height = 40
	m.recalculateLayout()

	view := m.pipelineScreen.viewPlanDiff(100)
	if !strings.Contains(view, "Plan Diff") {
		t.Error("expected diff header")
	}
	if !strings.Contains(view, "-Old.") || !strings.Contains(view, "+New.") {
		t.Error("expected viewport content in view")
	}
}
