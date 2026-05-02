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
		Plan: func(_ context.Context, _ string, _ io.Writer) (types.Specification, error) {
			return testSpec(), nil
		},
		Execute: func(_ context.Context, _ types.Specification, _ io.Writer) error {
			return nil
		},
	}
}

func TestNewModel_InitialState(t *testing.T) {
	m := NewModel(testPipeline())
	if m.state != StateIdle {
		t.Errorf("expected StateIdle, got %d", m.state)
	}
	if m.approved {
		t.Error("expected approved=false initially")
	}
	if len(m.tabsView.tabs) != 0 {
		t.Errorf("expected 0 tabs initially (idle), got %d", len(m.tabsView.tabs))
	}
}

func TestModel_PromptSubmitTransitionsToPlanning(t *testing.T) {
	m := NewModel(testPipeline())
	updated, _ := m.Update(PromptSubmitMsg{Prompt: "build a thing"})
	model := updated.(Model)
	if model.state != StatePlanning {
		t.Errorf("expected StatePlanning, got %d", model.state)
	}
	if model.prompt != "build a thing" {
		t.Errorf("expected prompt stored, got %q", model.prompt)
	}
	if model.planTabIdx == -1 {
		t.Error("expected planner tab to be created")
	}
}

func TestModel_PromptSubmitIgnoredWhenNotIdle(t *testing.T) {
	m := NewModel(testPipeline())
	m.state = StatePlanning

	updated, _ := m.Update(PromptSubmitMsg{Prompt: "ignored"})
	model := updated.(Model)
	if model.prompt != "" {
		t.Errorf("expected prompt unchanged when not idle, got %q", model.prompt)
	}
}

func TestModel_PlanCompleteTransitionsToConfirming(t *testing.T) {
	m := NewModel(testPipeline())
	m.state = StatePlanning
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

func TestModel_ConfirmRejected_TransitionsToIdle(t *testing.T) {
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
	// cmd should produce CycleBackToIdleMsg
	if cmd == nil {
		t.Fatal("expected cycle-back command")
	}
	msg := cmd()
	if _, ok := msg.(CycleBackToIdleMsg); !ok {
		t.Errorf("expected CycleBackToIdleMsg, got %T", msg)
	}
}

func TestModel_CycleBackToIdle(t *testing.T) {
	m := NewModel(testPipeline())
	m.state = StateDone

	updated, _ := m.Update(CycleBackToIdleMsg{})
	model := updated.(Model)
	if model.state != StateIdle {
		t.Errorf("expected StateIdle after cycle back, got %d", model.state)
	}
}

func TestModel_StateDoneTransitionsToIdle(t *testing.T) {
	// Full flow: Idle -> Planning -> Confirming -> Rejected -> Done -> Idle
	m := NewModel(testPipeline())

	// Submit prompt
	updated, _ := m.Update(PromptSubmitMsg{Prompt: "test"})
	model := updated.(Model)
	if model.state != StatePlanning {
		t.Fatalf("expected StatePlanning, got %d", model.state)
	}

	// Plan completes
	updated, _ = model.Update(planCompleteMsg{spec: testSpec()})
	model = updated.(Model)
	if model.state != StateConfirming {
		t.Fatalf("expected StateConfirming, got %d", model.state)
	}

	// Reject
	updated, cmd := model.Update(ConfirmMsg{Approved: false})
	model = updated.(Model)
	if model.state != StateDone {
		t.Fatalf("expected StateDone, got %d", model.state)
	}

	// Apply the cycle-back cmd
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)
	if model.state != StateIdle {
		t.Errorf("expected StateIdle after done->cycle, got %d", model.state)
	}
}

func TestModel_CtrlCQuits(t *testing.T) {
	m := NewModel(testPipeline())

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Error("expected quit command on ctrl+c")
	}
}

func TestModel_HarnessDoneTransitions(t *testing.T) {
	m := NewModel(testPipeline())
	m.state = StateExecuting
	m.execTabIdx = m.tabsView.AddTab("Worker")

	updated, cmd := m.Update(HarnessDoneMsg{TabIndex: m.execTabIdx, Err: nil})
	model := updated.(Model)
	if model.state != StateDone {
		t.Errorf("expected StateDone, got %d", model.state)
	}
	if model.err != nil {
		t.Errorf("expected nil error, got %v", model.err)
	}
	// Should produce CycleBackToIdleMsg
	if cmd == nil {
		t.Fatal("expected cycle-back command")
	}
	msg := cmd()
	if _, ok := msg.(CycleBackToIdleMsg); !ok {
		t.Errorf("expected CycleBackToIdleMsg, got %T", msg)
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

func TestModel_ToggleLogs(t *testing.T) {
	m := NewModel(testPipeline())
	if m.showLogs {
		t.Error("expected logs hidden by default")
	}

	updated, _ := m.Update(ToggleLogsMsg{})
	model := updated.(Model)
	if !model.showLogs {
		t.Error("expected logs visible after toggle")
	}

	updated, _ = model.Update(ToggleLogsMsg{})
	model = updated.(Model)
	if model.showLogs {
		t.Error("expected logs hidden after second toggle")
	}
}

func TestModel_CommandHelp(t *testing.T) {
	m := NewModel(testPipeline())
	updated, _ := m.Update(CommandMsg{Name: "/help", Args: ""})
	model := updated.(Model)
	if model.helpContent == "" {
		t.Error("expected help content to be set")
	}
}

func TestModel_PlanValidationPass(t *testing.T) {
	m := NewModel(testPipeline())
	m.state = StateValidating
	m.spec = testSpec()

	updated, _ := m.Update(PlanValidatedMsg{
		Report: &types.ValidationReport{
			SchemaVersion: "1",
			Verdict:       types.VerdictPass,
			Summary:       "looks good",
		},
	})
	model := updated.(Model)
	if model.state != StateConfirming {
		t.Errorf("expected StateConfirming after validation pass, got %d", model.state)
	}
}

func TestModel_PlanValidationFail(t *testing.T) {
	m := NewModel(testPipeline())
	m.state = StateValidating
	m.spec = testSpec()

	updated, cmd := m.Update(PlanValidatedMsg{
		Report: &types.ValidationReport{
			SchemaVersion: "1",
			Verdict:       types.VerdictFail,
			Summary:       "plan is bad",
		},
	})
	model := updated.(Model)
	if model.state != StateDone {
		t.Errorf("expected StateDone after validation fail, got %d", model.state)
	}
	if model.err == nil {
		t.Error("expected error to be set on validation failure")
	}
	if cmd != nil {
		t.Error("expected nil command (error held on screen until key dismiss)")
	}
}

func TestModel_PlanValidationSkipWhenNoValidator(t *testing.T) {
	m := NewModel(testPipeline())
	m.state = StatePlanning
	updated, _ := m.Update(planCompleteMsg{spec: testSpec()})
	model := updated.(Model)
	if model.state != StateConfirming {
		t.Errorf("expected StateConfirming when no validator, got %d", model.state)
	}
}

func TestModel_WorkValidationPass(t *testing.T) {
	m := NewModel(testPipeline())
	m.state = StateExecuting
	m.spec = testSpec()

	updated, cmd := m.Update(WorkValidatedMsg{
		Report: &types.ValidationReport{
			SchemaVersion: "1",
			Verdict:       types.VerdictPass,
			Summary:       "work is good",
		},
	})
	model := updated.(Model)
	if model.state != StateDone {
		t.Errorf("expected StateDone after work validation pass, got %d", model.state)
	}
	if cmd == nil {
		t.Error("expected cycle-back command")
	}
}
