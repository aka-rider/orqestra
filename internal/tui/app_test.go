package tui

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// noopRunner is a no-op CLIRunner for tests that don't need real LLM calls.
type noopRunner struct{}

func (n *noopRunner) RunPrint(_ context.Context, _, _ string) (harness.RunResult, error) {
	return harness.RunResult{Output: `{"verdict":"accept","brief":{"task":"t","end_state":"e","deliverables":[],"scope":[],"non_scope":[],"acceptance_hints":[]},"questions":[],"confidence":0.9,"planner_question":"How?"}`}, nil
}

func (n *noopRunner) RunStreaming(_ context.Context, _, _ string, _ io.Writer) (harness.RunResult, error) {
	return harness.RunResult{Output: `{"verdict":"accept","brief":{"task":"t","end_state":"e","deliverables":[],"scope":[],"non_scope":[],"acceptance_hints":[]},"questions":[],"confidence":0.9,"planner_question":"How?"}`}, nil
}

// testModel creates a Model suitable for testing with a minimal mock engine.
func testModel() Model {
	engine := &orchestrator.Engine{
		Config: testConfig(),
		Runners: orchestrator.Runners{
			Gateway:   &noopRunner{},
			Planner:   &noopRunner{},
			Validator: &noopRunner{},
			Worker:    &noopRunner{},
			QA:        &noopRunner{},
		},
	}
	m := NewModel(engine)
	m.width = 120
	m.height = 40
	return m
}

func testConfig() *config.Config {
	return &config.Config{
		Gateway:   config.GatewayConfig{SystemPrompt: "test"},
		Planner:   config.PlannerConfig{},
		Validator: config.ValidatorConfig{},
		Worker:    config.WorkerConfig{},
		QA:        config.ValidatorConfig{},
	}
}

func sendKey(m tea.Model, key tea.KeyType) (tea.Model, tea.Cmd) {
	return m.Update(tea.KeyMsg{Type: key})
}

func sendRune(m tea.Model, r string) (tea.Model, tea.Cmd) {
	return m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(r)})
}

func TestTUI_PromptSubmit(t *testing.T) {
	m := testModel()
	// Set prompt value directly (textarea handles rune input internally)
	m.prompt.SetValue("add a feature")

	// Press Enter to submit
	result, cmd := sendKey(m, tea.KeyEnter)
	model := result.(Model)

	if model.state != StatePipeline {
		t.Errorf("expected StatePipeline, got %d", model.state)
	}
	if model.content != ContentStreaming {
		t.Errorf("expected ContentStreaming, got %d", model.content)
	}
	if model.goal != "add a feature" {
		t.Errorf("expected goal 'add a feature', got %q", model.goal)
	}
	// Cmd should be non-nil (waitForEvent)
	_ = cmd
}

func TestTUI_PromptSkipGateway(t *testing.T) {
	m := testModel()
	m.prompt.SetValue("add a feature")

	result, _ := sendKey(m, tea.KeyCtrlS)
	model := result.(Model)

	if model.state != StatePipeline {
		t.Errorf("expected StatePipeline, got %d", model.state)
	}
	if model.goal != "add a feature" {
		t.Errorf("expected goal 'add a feature', got %q", model.goal)
	}
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
	m.content = ContentStreaming
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

	result, _ := m.handleOrchestratorEvent(event)
	model := result.(Model)

	if model.content != ContentCoaching {
		t.Errorf("expected ContentCoaching, got %d", model.content)
	}
	if len(model.answerFields) != 1 {
		t.Fatalf("expected 1 answer field, got %d", len(model.answerFields))
	}
	if model.answerFields[0].Value() != "login" {
		t.Errorf("expected default 'login', got %q", model.answerFields[0].Value())
	}
}

func TestTUI_CoachingSubmit(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.content = ContentCoaching
	decisions := make(chan orchestrator.Decision, 1)
	m.decisions = decisions
	m.gatewayResult = agent.GatewayResult{
		Questions: []agent.Question{
			{Text: "Which part?", Options: []string{"a", "b"}, Default: "a"},
		},
	}

	// Create answer field with value
	m.answerFields = makeAnswerFields(m.gatewayResult.Questions, m.effectiveWidth())
	m.answerFields[0].SetValue("module b")
	m.answerCursor = 0

	// Press Enter to submit
	result, _ := sendKey(m, tea.KeyEnter)
	model := result.(Model)

	if model.content != ContentStreaming {
		t.Errorf("expected ContentStreaming after submit, got %d", model.content)
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
	m.content = ContentCoaching
	decisions := make(chan orchestrator.Decision, 1)
	m.decisions = decisions
	m.gatewayResult = agent.GatewayResult{
		Questions: []agent.Question{
			{Text: "Q1?", Options: []string{"a"}, Default: "a"},
		},
	}
	m.answerFields = makeAnswerFields(m.gatewayResult.Questions, m.effectiveWidth())
	m.answerCursor = 0

	result, _ := sendKey(m, tea.KeyCtrlS)
	model := result.(Model)

	if model.content != ContentStreaming {
		t.Errorf("expected ContentStreaming after skip, got %d", model.content)
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
	m.content = ContentStreaming
	m.events = make(chan orchestrator.Event, 1)

	event := orchestrator.Event{
		Type: orchestrator.EventGateRequest,
		Gate: orchestrator.GateRequest{
			Type: orchestrator.GatePlanApproval,
			PlanOutput: agent.PlanOutput{
				Spec: agent.Specification{Goal: "Add feature X", Steps: []string{"Step 1"}},
			},
		},
	}

	result, _ := m.handleOrchestratorEvent(event)
	model := result.(Model)

	if model.content != ContentPlanReview {
		t.Errorf("expected ContentPlanReview, got %d", model.content)
	}
	if !model.hasPlan {
		t.Error("expected hasPlan=true")
	}
}

func TestTUI_PlanApprove(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.content = ContentPlanReview
	m.hasPlan = true
	m.planOutput = agent.PlanOutput{Spec: agent.Specification{Goal: "Test"}}
	decisions := make(chan orchestrator.Decision, 1)
	m.decisions = decisions

	result, _ := sendRune(m, "a")
	model := result.(Model)

	if model.content != ContentStreaming {
		t.Errorf("expected ContentStreaming after approve, got %d", model.content)
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
	m.content = ContentPlanReview
	m.hasPlan = true
	m.planOutput = agent.PlanOutput{Spec: agent.Specification{Goal: "Original"}}
	decisions := make(chan orchestrator.Decision, 1)
	m.decisions = decisions

	// Press E to enter edit mode
	result, _ := sendRune(m, "e")
	model := result.(Model)

	if model.content != ContentPlanEdit {
		t.Fatalf("expected ContentPlanEdit, got %d", model.content)
	}
	if !model.hasPlanEditor {
		t.Fatal("expected hasPlanEditor=true")
	}

	// Simulate editing the content
	model.planEditor.SetValue(`{"goal":"Modified","steps":["s1"],"acceptance":["a1"]}`)

	// Press Ctrl+S to save
	result2, _ := sendKey(model, tea.KeyCtrlS)
	model2 := result2.(Model)

	if model2.content != ContentStreaming {
		t.Errorf("expected ContentStreaming after save, got %d", model2.content)
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
	m.content = ContentPlanReview
	m.hasPlan = true
	m.planOutput = agent.PlanOutput{Spec: agent.Specification{Goal: "Original"}}

	// Press E to enter edit mode
	result, _ := sendRune(m, "e")
	model := result.(Model)

	if model.content != ContentPlanEdit {
		t.Fatal("expected ContentPlanEdit")
	}

	// Press Esc to discard
	result2, _ := sendKey(model, tea.KeyEsc)
	model2 := result2.(Model)

	if model2.content != ContentPlanReview {
		t.Errorf("expected ContentPlanReview after discard, got %d", model2.content)
	}
}

func TestTUI_CancelAgent(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.content = ContentStreaming
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
	m.content = ContentStreaming
	m.agents = []AgentRow{
		{ID: "gateway", State: "done"},
		{ID: "planner", State: "running"},
	}

	// Press 1 to view gateway history
	result, _ := sendRune(m, "1")
	model := result.(Model)

	if model.content != ContentAgentHistory {
		t.Errorf("expected ContentAgentHistory, got %d", model.content)
	}
	if model.focusedAgent != 1 {
		t.Errorf("expected focusedAgent=1, got %d", model.focusedAgent)
	}
}

func TestTUI_AgentNavBack(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.content = ContentAgentHistory
	m.focusedAgent = 1
	m.agents = []AgentRow{{ID: "gateway", State: "done"}}

	// Press Esc to go back
	result, _ := sendKey(m, tea.KeyEsc)
	model := result.(Model)

	if model.content != ContentStreaming {
		t.Errorf("expected ContentStreaming after Esc, got %d", model.content)
	}
	if model.focusedAgent != 0 {
		t.Errorf("expected focusedAgent=0, got %d", model.focusedAgent)
	}
}

func TestTUI_NewRun(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.content = ContentCompletion
	m.goal = "original goal"

	result, _ := sendRune(m, "n")
	model := result.(Model)

	if model.state != StatePrompt {
		t.Errorf("expected StatePrompt, got %d", model.state)
	}
	if !strings.Contains(model.prompt.Value(), "original goal") {
		t.Errorf("expected prompt pre-filled with goal, got %q", model.prompt.Value())
	}
}

func TestTUI_NewRunConfirm(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.content = ContentStreaming
	m.goal = "active task"
	cancelled := false
	m.cancel = func() { cancelled = true }

	// Press N during active pipeline — should show confirmation
	result, _ := sendRune(m, "n")
	model := result.(Model)

	if model.state != StatePipeline {
		t.Error("expected to stay in StatePipeline until confirmed")
	}
	if !model.confirmNew {
		t.Error("expected confirmNew=true")
	}

	// View should show confirmation message
	view := model.View()
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
	if !strings.Contains(model2.prompt.Value(), "active task") {
		t.Errorf("expected prompt pre-filled with goal, got %q", model2.prompt.Value())
	}
}

func TestTUI_SidebarUpdates(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.content = ContentStreaming
	m.events = make(chan orchestrator.Event, 1)

	// AgentStarted
	result, _ := m.handleOrchestratorEvent(orchestrator.Event{
		Type:    orchestrator.EventAgentStarted,
		AgentID: "gateway",
	})
	model := result.(Model)

	if len(model.agents) != 1 || model.agents[0].State != "running" {
		t.Errorf("expected 1 agent running, got %+v", model.agents)
	}

	// AgentDone
	result2, _ := model.handleOrchestratorEvent(orchestrator.Event{
		Type:    orchestrator.EventAgentDone,
		AgentID: "gateway",
	})
	model2 := result2.(Model)

	if model2.agents[0].State != "done" {
		t.Errorf("expected agent state 'done', got %q", model2.agents[0].State)
	}
}

func TestTUI_FullDashboard(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.content = ContentStreaming
	m.agents = []AgentRow{
		{ID: "gateway", State: "done"},
		{ID: "planner", State: "running"},
	}

	// Press D to toggle dashboard
	result, _ := sendRune(m, "d")
	model := result.(Model)

	if !model.showDashboard {
		t.Error("expected showDashboard=true")
	}

	// View should contain dashboard content
	view := model.View()
	if !strings.Contains(view, "gateway") || !strings.Contains(view, "planner") {
		t.Error("expected dashboard to show agent names")
	}

	// Press Esc to return
	result2, _ := sendKey(model, tea.KeyEsc)
	model2 := result2.(Model)

	if model2.showDashboard {
		t.Error("expected showDashboard=false after Esc")
	}
}

func TestTUI_DoubleCtrlC(t *testing.T) {
	m := testModel()

	// First Ctrl+C
	result, cmd := sendKey(m, tea.KeyCtrlC)
	model := result.(Model)
	if cmd != nil {
		t.Error("first Ctrl+C should not quit")
	}
	if model.ctrlC != 1 {
		t.Errorf("expected ctrlC=1, got %d", model.ctrlC)
	}

	// Second Ctrl+C
	_, cmd = sendKey(model, tea.KeyCtrlC)
	if cmd == nil {
		t.Error("second Ctrl+C should trigger quit")
	}
}

func TestTUI_CompletionQA(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.content = ContentStreaming
	m.events = make(chan orchestrator.Event, 1)

	event := orchestrator.Event{
		Type:  orchestrator.EventComplete,
		HasQA: true,
		QAReport: agent.ValidationReport{
			Verdict: agent.VerdictPass,
			Summary: "All criteria met",
		},
	}

	result, _ := m.handleOrchestratorEvent(event)
	model := result.(Model)

	if model.content != ContentCompletion {
		t.Errorf("expected ContentCompletion, got %d", model.content)
	}
	if !model.hasQA {
		t.Error("expected hasQA=true")
	}
	if model.qaReport.Verdict != agent.VerdictPass {
		t.Errorf("expected pass verdict, got %q", model.qaReport.Verdict)
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
