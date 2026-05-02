package tui

import (
	"context"
	"fmt"
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

	updated, _ := m.Update(ConfirmMsg{Choice: ConfirmAccept})
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

	updated, cmd := m.Update(ConfirmMsg{Choice: ConfirmReject})
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
	updated, cmd := model.Update(ConfirmMsg{Choice: ConfirmReject})
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

// TestModel_HarnessDoneWithValidateWorkResult_TransitionsToWorkValidating verifies
// that when ValidateWorkResult is configured, HarnessDoneMsg transitions to
// StateWorkValidating (not StateDone).
func TestModel_HarnessDoneWithValidateWorkResult_TransitionsToWorkValidating(t *testing.T) {
	pipeline := testPipeline()
	pipeline.ValidateWorkResult = func(_ context.Context, _ types.Specification, _ types.WorkOutput) (types.ValidationResult, error) {
		return types.ValidationResult{Passed: true, Score: 1.0}, nil
	}

	m := NewModel(pipeline)
	m.state = StateExecuting
	m.spec = testSpec()
	m.execTabIdx = m.tabsView.AddTab("Worker")

	updated, _ := m.Update(HarnessDoneMsg{TabIndex: m.execTabIdx, Err: nil, WorkOutput: "done\n"})
	model := updated.(Model)

	if model.state != StateWorkValidating {
		t.Errorf("expected StateWorkValidating, got %d", model.state)
	}
}

// TestModel_ValidationResultMsg_PassedTransitionsToDone verifies a passing
// ValidationResultMsg transitions to StateDone with no error stored.
func TestModel_ValidationResultMsg_PassedTransitionsToDone(t *testing.T) {
	m := NewModel(testPipeline())
	m.state = StateWorkValidating

	updated, _ := m.Update(ValidationResultMsg{
		Result: types.ValidationResult{Passed: true, Score: 1.0},
	})
	model := updated.(Model)

	if model.state != StateDone {
		t.Errorf("expected StateDone, got %d", model.state)
	}
	if model.err != nil {
		t.Errorf("expected nil error for passed validation, got %v", model.err)
	}
	if !model.validateView.done {
		t.Error("expected validateView.done=true")
	}
}

// TestModel_ValidationResultMsg_FailedTransitionsToDone verifies that a failing
// ValidationResultMsg transitions to StateDone with an error that describes
// how many criteria were not met.
func TestModel_ValidationResultMsg_FailedTransitionsToDone(t *testing.T) {
	m := NewModel(testPipeline())
	m.state = StateWorkValidating

	updated, _ := m.Update(ValidationResultMsg{
		Result: types.ValidationResult{
			Passed: false,
			Score:  0.0,
			FailedCriteria: []types.FailedCriterion{
				{Criterion: "Criterion 1", Reason: "not implemented"},
			},
		},
	})
	model := updated.(Model)

	if model.state != StateDone {
		t.Errorf("expected StateDone, got %d", model.state)
	}
	if model.err == nil {
		t.Error("expected non-nil error for failed validation")
	}
	if !model.validateView.done {
		t.Error("expected validateView.done=true")
	}
}

// TestModel_ValidationResultMsg_ErrTransitionsToDone verifies that a
// ValidationResultMsg with a non-nil Err stores the exact error on the model.
func TestModel_ValidationResultMsg_ErrTransitionsToDone(t *testing.T) {
	m := NewModel(testPipeline())
	m.state = StateWorkValidating

	expectedErr := fmt.Errorf("network error: connection refused")
	updated, _ := m.Update(ValidationResultMsg{Err: expectedErr})
	model := updated.(Model)

	if model.state != StateDone {
		t.Errorf("expected StateDone, got %d", model.state)
	}
	if model.err != expectedErr {
		t.Errorf("expected error %v, got %v", expectedErr, model.err)
	}
	if !model.validateView.done {
		t.Error("expected validateView.done=true")
	}
}

// TestModel_StateWorkValidating_FullSequence exercises the state machine path:
// StateExecuting → StateWorkValidating → StateDone.
func TestModel_StateWorkValidating_FullSequence(t *testing.T) {
	pipeline := testPipeline()
	pipeline.ValidateWorkResult = func(_ context.Context, _ types.Specification, _ types.WorkOutput) (types.ValidationResult, error) {
		return types.ValidationResult{Passed: true, Score: 1.0}, nil
	}

	m := NewModel(pipeline)

	// Simulate: submit prompt → plan completes → confirm → execution starts
	updated, _ := m.Update(PromptSubmitMsg{Prompt: "build it"})
	m = updated.(Model)
	if m.state != StatePlanning {
		t.Fatalf("expected StatePlanning, got %d", m.state)
	}

	updated, _ = m.Update(planCompleteMsg{spec: testSpec()})
	m = updated.(Model)
	if m.state != StateConfirming {
		t.Fatalf("expected StateConfirming, got %d", m.state)
	}

	updated, _ = m.Update(ConfirmMsg{Choice: ConfirmAccept})
	m = updated.(Model)
	if m.state != StateExecuting {
		t.Fatalf("expected StateExecuting, got %d", m.state)
	}

	// Worker finishes — should move to StateWorkValidating
	m.execTabIdx = 0
	updated, _ = m.Update(HarnessDoneMsg{TabIndex: 0, WorkOutput: "done"})
	m = updated.(Model)
	if m.state != StateWorkValidating {
		t.Fatalf("expected StateWorkValidating, got %d", m.state)
	}

	// Validation result arrives
	updated, _ = m.Update(ValidationResultMsg{
		Result: types.ValidationResult{Passed: true, Score: 1.0},
	})
	m = updated.(Model)
	if m.state != StateDone {
		t.Errorf("expected StateDone, got %d", m.state)
	}
}
