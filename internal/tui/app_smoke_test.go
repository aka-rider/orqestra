package tui

import (
	"fmt"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// hydratedModels returns a named set of models covering all stateful TUI views.
func hydratedModels(t *testing.T) map[string]Model {
	t.Helper()

	questions := []agent.Question{
		{Text: "Which part?", Options: []string{"login", "signup"}, Default: "login"},
	}

	base := func() Model {
		m := testModel()
		m.pipelineScreen.goal = "test goal"
		m.pipelineScreen.phase = orchestrator.Phase("executing")
		return m
	}

	models := make(map[string]Model)

	// StatePipeline + ContentCoaching
	{
		m := base()
		m.state = StatePipeline
		m.pipelineScreen.content = ContentCoaching
		m.pipelineScreen.gatewayResult = agent.GatewayResult{
			Verdict:   agent.GatewayVerdictCoach,
			Brief:     agent.PromptBrief{Task: "Improve auth"},
			Questions: questions,
		}
		m.pipelineScreen.answerFields = makeAnswerFields(questions, m.width)
		m.pipelineScreen.answerCursor = 0
		models["pipeline-coaching"] = m
	}

	// StatePipeline + ContentPlanReview
	{
		m := base()
		m.state = StatePipeline
		m.pipelineScreen.content = ContentPlanReview
		m.pipelineScreen.hasPlan = true
		m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nDo the thing."
		m.pipelineScreen.hasPlanComment = true
		m.pipelineScreen.planComment = textarea.New()
		m.pipelineScreen.planComment.SetWidth(80)
		m.pipelineScreen.planComment.SetHeight(2)
		m.pipelineScreen.planComment.CharLimit = 1024
		m.pipelineScreen.planComment.Focus()
		models["pipeline-plan-review"] = m
	}

	// StatePipeline + ContentPlanEdit
	{
		m := base()
		m.state = StatePipeline
		m.pipelineScreen.content = ContentPlanEdit
		m.pipelineScreen.hasPlan = true
		m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nEdit me."
		m.pipelineScreen.hasPlanEditor = true
		m.pipelineScreen.planEditor = textarea.New()
		m.pipelineScreen.planEditor.SetWidth(100)
		m.pipelineScreen.planEditor.SetHeight(30)
		m.pipelineScreen.planEditor.SetValue(m.pipelineScreen.finalPlan)
		m.pipelineScreen.planEditor.Focus()
		models["pipeline-plan-edit"] = m
	}

	// StatePipeline + ContentAgentHistory
	{
		m := base()
		m.state = StatePipeline
		m.pipelineScreen.content = ContentAgentHistory
		m.pipelineScreen.agents = []AgentRow{
			{ID: "gateway", State: "done", Elapsed: 10 * time.Second, StartedAt: time.Now().Add(-10 * time.Second), InputTokens: 500, OutputTokens: 200},
			{ID: "researcher", State: "running", Elapsed: 30 * time.Second, StartedAt: time.Now().Add(-30 * time.Second), InputTokens: 2000, OutputTokens: 1000},
		}
		m.pipelineScreen.focusedAgent = 1
		models["pipeline-agent-history"] = m
	}

	// StatePipeline + ContentCompletion
	{
		m := base()
		m.state = StatePipeline
		m.pipelineScreen.content = ContentCompletion
		m.pipelineScreen.hasPlan = true
		m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nDone."
		m.pipelineScreen.hasValidation = true
		m.pipelineScreen.workerValidation = "pass"
		m.pipelineScreen.agents = []AgentRow{
			{ID: "gateway", State: "done", Elapsed: 5 * time.Second, StartedAt: time.Now().Add(-5 * time.Second), InputTokens: 300, OutputTokens: 100},
		}
		models["pipeline-completion"] = m
	}

	// StateRunsList
	{
		m := base()
		m.state = StateRunsList
		m.runsListScreen.SetRuns(testRunSummaries())
		models["runs-list"] = m
	}

	// StateRunDetail
	{
		m := base()
		m.state = StateRunDetail
		m.runDetailScreen.SetDetail(testRunDetail())
		m.runDetailScreen.logLines = []string{"line1", "line2"}
		models["run-detail"] = m
	}

	return models
}

func TestLayout_AllStatesRenderWithoutPanic(t *testing.T) {
	sizes := []struct {
		w, h int
	}{
		{60, 10},
		{120, 40},
		{200, 60},
		{1, 1},
	}

	for name, m := range hydratedModels(t) {
		for _, sz := range sizes {
			t.Run(fmt.Sprintf("%s/%dx%d", name, sz.w, sz.h), func(t *testing.T) {
				m := m // capture
				m.width = sz.w
				m.height = sz.h

				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("panic during render: %v", r)
					}
				}()

				m.recalculateLayout()
				_ = m.View()
			})
		}
	}
}

func TestLayout_CtrlCAlwaysQuits(t *testing.T) {
	for name, m := range hydratedModels(t) {
		t.Run(name, func(t *testing.T) {
			m := m
			m.recalculateLayout()

			// First Ctrl+C — should not quit yet.
			result, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
			m = result.(Model)

			// Second Ctrl+C — should produce a quit command.
			result, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
			_ = result.(Model)

			if cmd == nil {
				t.Fatal("expected non-nil cmd after second Ctrl+C")
			}
			msg := cmd()
			if _, ok := msg.(tea.QuitMsg); !ok {
				t.Fatalf("expected tea.QuitMsg, got %T", msg)
			}
		})
	}
}

func TestLayout_EditorReturnError(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentPlanReview
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = "# Plan"
	m.pipelineScreen.editorRunning = true
	m.pipelineScreen.goal = "test goal"
	m.pipelineScreen.phase = orchestrator.Phase("executing")
	m.width = 120
	m.height = 40
	m.recalculateLayout()

	result, _ := m.Update(editorReturnMsg{err: fmt.Errorf("editor failed")})
	model := result.(Model)

	if model.pipelineScreen.lastErr == nil {
		t.Fatal("expected lastErr to be set after editor error")
	}
	if model.pipelineScreen.editorRunning {
		t.Error("expected editorRunning to be false after editor return")
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic during View after editor error: %v", r)
		}
	}()
	_ = model.View()
}
