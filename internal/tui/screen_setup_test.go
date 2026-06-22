package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/orchestrator"
)

func pressKey(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code}
}

func pressSpace() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: ' '}
}

func TestSetupModel_Navigation(t *testing.T) {
	m := newSetupModel()
	m.Open(orchestrator.DefaultPipelineSetup())

	// Down wraps through all items
	for i := 1; i < numSetupItems; i++ {
		m, _ = m.Update(pressKey(tea.KeyDown))
		if m.cursor != i {
			t.Errorf("after %d downs: cursor = %d, want %d", i, m.cursor, i)
		}
	}
	// One more wraps to 0
	m, _ = m.Update(pressKey(tea.KeyDown))
	if m.cursor != 0 {
		t.Errorf("wrap: cursor = %d, want 0", m.cursor)
	}

	// Up from 0 wraps to last
	m, _ = m.Update(pressKey(tea.KeyUp))
	if m.cursor != numSetupItems-1 {
		t.Errorf("up wrap: cursor = %d, want %d", m.cursor, numSetupItems-1)
	}
}

func TestSetupModel_ToggleBool(t *testing.T) {
	m := newSetupModel()
	m.Open(orchestrator.DefaultPipelineSetup())

	// Navigate to Execution (cursor 1; cursor 0 is the Deliberation int).
	m, _ = m.Update(pressKey(tea.KeyDown))
	if m.cursor != setupItemExecution {
		t.Fatalf("cursor = %d, want %d (Execution)", m.cursor, setupItemExecution)
	}
	if !m.setup.Execution {
		t.Fatal("Execution should start enabled")
	}
	m, _ = m.Update(pressKey(tea.KeyLeft))
	if m.setup.Execution {
		t.Error("Execution should be disabled after Left")
	}
	m, _ = m.Update(pressKey(tea.KeyRight))
	if !m.setup.Execution {
		t.Error("Execution should be re-enabled after Right")
	}

	// Space also toggles
	m, _ = m.Update(pressSpace())
	if m.setup.Execution {
		t.Error("Execution should be disabled after Space")
	}
}

func TestSetupModel_GateToggle(t *testing.T) {
	m := newSetupModel()
	m.Open(orchestrator.DefaultPipelineSetup())
	// Move to first gate item (cursor=setupItemGateFirst, GateAfterDeliberation)
	for i := 0; i < setupItemGateFirst; i++ {
		m, _ = m.Update(pressKey(tea.KeyDown))
	}

	// GateAfterDeliberation is on by default — toggle off
	if !m.setup.HumanGates.Active(orchestrator.GateAfterDeliberation) {
		t.Fatal("GateAfterDeliberation should start active")
	}
	m, _ = m.Update(pressKey(tea.KeyLeft))
	if m.setup.HumanGates.Active(orchestrator.GateAfterDeliberation) {
		t.Error("GateAfterDeliberation should be inactive after toggle")
	}
	m, _ = m.Update(pressKey(tea.KeyLeft))
	if !m.setup.HumanGates.Active(orchestrator.GateAfterDeliberation) {
		t.Error("GateAfterDeliberation should be active again after second toggle")
	}
}

func TestSetupModel_EnterEmitsConfirmAndCloses(t *testing.T) {
	m := newSetupModel()
	setup := orchestrator.DefaultPipelineSetup()
	setup.Execution = false
	m.Open(setup)

	m, _ = m.Update(pressKey(tea.KeyEnter))

	if m.IsOpen() {
		t.Error("panel should be closed after Enter")
	}
	intent, ok := m.PendingIntent.(ConfirmSetupIntent)
	if !ok {
		t.Fatalf("PendingIntent = %T, want ConfirmSetupIntent", m.PendingIntent)
	}
	if intent.Setup.Execution {
		t.Error("confirmed setup should have Execution=false")
	}
}

func TestSetupModel_EscClosesWithoutEmitting(t *testing.T) {
	m := newSetupModel()
	m.Open(orchestrator.DefaultPipelineSetup())

	m, _ = m.Update(pressKey(tea.KeyEscape))

	if m.IsOpen() {
		t.Error("panel should be closed after Esc")
	}
	if m.PendingIntent != nil {
		t.Errorf("PendingIntent should be nil after Esc, got %T", m.PendingIntent)
	}
}

func TestSetupModel_ViewPurity(t *testing.T) {
	m := newSetupModel()
	m.Open(orchestrator.DefaultPipelineSetup())

	v1 := m.View()
	v2 := m.View()
	if v1 != v2 {
		t.Error("View() must be pure — same state produced different output")
	}
}

func TestSetupModel_DeliberationIncrement(t *testing.T) {
	m := newSetupModel()
	m.Open(orchestrator.DefaultPipelineSetup())

	// Deliberation is the first item (cursor 0).
	if m.cursor != setupItemDeliberation {
		t.Fatalf("cursor = %d, want %d", m.cursor, setupItemDeliberation)
	}
	if m.setup.DeliberationRounds != 1 {
		t.Fatalf("DeliberationRounds = %d, want 1", m.setup.DeliberationRounds)
	}

	// Right → 2
	m, _ = m.Update(pressKey(tea.KeyRight))
	if m.setup.DeliberationRounds != 2 {
		t.Errorf("after right: DeliberationRounds = %d, want 2", m.setup.DeliberationRounds)
	}

	// Right → 3
	m, _ = m.Update(pressKey(tea.KeyRight))
	if m.setup.DeliberationRounds != 3 {
		t.Errorf("after right: DeliberationRounds = %d, want 3", m.setup.DeliberationRounds)
	}

	// Right → clamp at 3
	m, _ = m.Update(pressKey(tea.KeyRight))
	if m.setup.DeliberationRounds != 3 {
		t.Errorf("after right (clamp): DeliberationRounds = %d, want 3", m.setup.DeliberationRounds)
	}

	// Left → 2
	m, _ = m.Update(pressKey(tea.KeyLeft))
	if m.setup.DeliberationRounds != 2 {
		t.Errorf("after left: DeliberationRounds = %d, want 2", m.setup.DeliberationRounds)
	}

	// Left → 1
	m, _ = m.Update(pressKey(tea.KeyLeft))
	if m.setup.DeliberationRounds != 1 {
		t.Errorf("after left: DeliberationRounds = %d, want 1", m.setup.DeliberationRounds)
	}

	// Left → clamp at 1
	m, _ = m.Update(pressKey(tea.KeyLeft))
	if m.setup.DeliberationRounds != 1 {
		t.Errorf("after left (clamp): DeliberationRounds = %d, want 1", m.setup.DeliberationRounds)
	}
}

func TestSetupModel_DeliberationInConfirm(t *testing.T) {
	m := newSetupModel()
	m.Open(orchestrator.DefaultPipelineSetup())

	// Deliberation is the first item (cursor 0). Change to 3.
	m, _ = m.Update(pressKey(tea.KeyRight))
	m, _ = m.Update(pressKey(tea.KeyRight))
	if m.setup.DeliberationRounds != 3 {
		t.Fatalf("DeliberationRounds = %d, want 3", m.setup.DeliberationRounds)
	}

	// Confirm
	m, _ = m.Update(pressKey(tea.KeyEnter))

	if m.IsOpen() {
		t.Error("panel should be closed after Enter")
	}
	intent, ok := m.PendingIntent.(ConfirmSetupIntent)
	if !ok {
		t.Fatalf("PendingIntent = %T, want ConfirmSetupIntent", m.PendingIntent)
	}
	if intent.Setup.DeliberationRounds != 3 {
		t.Errorf("confirmed setup.DeliberationRounds = %d, want 3", intent.Setup.DeliberationRounds)
	}
}
