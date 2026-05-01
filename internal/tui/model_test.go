package tui

import (
	"context"
	"io"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/xiii/orqestra/internal/types"
)

func testSpec() types.Specification {
	return types.Specification{
		Goal:       "Test goal",
		Steps:      []string{"Step 1", "Step 2"},
		Acceptance: []string{"Criterion 1", "Criterion 2"},
	}
}

func testPipeline() PipelineFuncs {
	return PipelineFuncs{
		Plan: func(_ context.Context, _ io.Writer) (types.Specification, error) {
			return testSpec(), nil
		},
		Execute: func(_ context.Context, _ types.Specification, _ io.Writer) error {
			return nil
		},
	}
}

func TestNewModel_InitialState(t *testing.T) {
	m := NewModel(testPipeline())
	if m.state != StatePlanning {
		t.Errorf("expected StatePlanning, got %d", m.state)
	}
	if m.approved {
		t.Error("expected approved=false initially")
	}
}

func TestModel_PlanCompleteTransitionsToConfirming(t *testing.T) {
	m := NewModel(testPipeline())
	updated, _ := m.Update(planCompleteMsg{spec: testSpec()})
	model := updated.(Model)
	if model.state != StateConfirming {
		t.Errorf("expected StateConfirming, got %d", model.state)
	}
	if model.spec.Goal != "Test goal" {
		t.Errorf("expected spec to be set, got %q", model.spec.Goal)
	}
}

func TestModel_ConfirmApproved(t *testing.T) {
	m := NewModel(testPipeline())
	m.state = StateConfirming
	m.spec = testSpec()

	updated, _ := m.Update(ConfirmMsg{Approved: true})
	model := updated.(Model)
	if model.state != StateExecuting {
		t.Errorf("expected StateExecuting, got %d", model.state)
	}
	if !model.approved {
		t.Error("expected approved=true")
	}
}

func TestModel_ConfirmRejected(t *testing.T) {
	m := NewModel(testPipeline())
	m.state = StateConfirming

	updated, cmd := m.Update(ConfirmMsg{Approved: false})
	model := updated.(Model)
	if model.state != StateDone {
		t.Errorf("expected StateDone, got %d", model.state)
	}
	if model.approved {
		t.Error("expected approved=false")
	}
	if cmd == nil {
		t.Error("expected quit command")
	}
}

func TestModel_CtrlCQuits(t *testing.T) {
	m := NewModel(testPipeline())
	m.state = StateConfirming

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Error("expected quit command on ctrl+c")
	}
}

func TestModel_HarnessDoneTransitions(t *testing.T) {
	m := NewModel(testPipeline())
	m.state = StateExecuting
	m.execTabIdx = m.tabsView.AddTab("Worker")

	updated, _ := m.Update(HarnessDoneMsg{TabIndex: m.execTabIdx, Err: nil})
	model := updated.(Model)
	if model.state != StateDone {
		t.Errorf("expected StateDone, got %d", model.state)
	}
	if model.err != nil {
		t.Errorf("expected nil error, got %v", model.err)
	}
}

func TestModel_LogMsg(t *testing.T) {
	m := NewModel(testPipeline())
	entry := LogEntry{Level: "INFO", Message: "test"}
	updated, _ := m.Update(LogMsg{Entry: entry})
	model := updated.(Model)
	if len(model.logPanel.entries) != 1 {
		t.Errorf("expected 1 log entry, got %d", len(model.logPanel.entries))
	}
}

func TestModel_AddTab(t *testing.T) {
	m := NewModel(testPipeline())
	// Model starts with 1 pre-created Planner tab
	if len(m.tabsView.tabs) != 1 {
		t.Errorf("expected 1 pre-created tab, got %d", len(m.tabsView.tabs))
	}
	updated, _ := m.Update(addTabMsg{name: "Worker"})
	model := updated.(Model)
	if len(model.tabsView.tabs) != 2 {
		t.Errorf("expected 2 tabs, got %d", len(model.tabsView.tabs))
	}
}
