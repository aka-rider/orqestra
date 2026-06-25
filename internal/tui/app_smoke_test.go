package tui

import (
	"fmt"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// hydratedModels returns a named set of models covering all stateful TUI views.
func hydratedModels(t *testing.T) map[string]Model {
	t.Helper()

	base := func() Model {
		m := testModel()
		m.pipelineScreen.goal = "test goal"
		m.pipelineScreen.phase = orchestrator.Phase("executing")
		return m
	}

	models := make(map[string]Model)

	// StatePipeline + ContentHumanGate
	{
		m := base()
		m.state = StatePipeline
		m.pipelineScreen.content = ContentHumanGate
		m.pipelineScreen.hasPlan = true
		m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nDo the thing."
		m.pipelineScreen.awaitingPlanDecision = true
		models["pipeline-human-gate"] = m
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
			{ID: "researcher", State: AgentStateDone, Elapsed: 30 * time.Second, StartedAt: time.Now().Add(-30 * time.Second), InputTokens: 2000, OutputTokens: 1000},
		}

		sb := orchestrator.NewStreamRing(50)
		sb.SetAgent("researcher")
		sb.AppendActivity("Read", "file.go")
		sb.SetAgent("architect") // snaps "researcher"
		m.pipelineScreen.SetStreamBuf(sb)

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
			result, _ := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
			m = result.(Model)

			// Second Ctrl+C — should produce a quit command.
			result, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
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
	m.pipelineScreen.content = ContentStreaming
	m.pipelineScreen.hasPlan = true
	m.pipelineScreen.finalPlan = "# Plan"
	m.pipelineScreen.editorFilePath = "/tmp/orqestra-plan-test.md"
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

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic during View after editor error: %v", r)
		}
	}()
	_ = model.View()
}
