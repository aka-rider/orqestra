//go:build darwin

package chrome

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

func testSnapshot() Snapshot {
	return Snapshot{
		Phase: PhaseWorker,
		Goal:  "Implement tmux-like passthrough mux",
		Tabs: []TabInfo{
			{Name: "Intake", Index: 0, State: TabStateDone, StartedAt: time.Now().Add(-2 * time.Minute), ExitCode: 0},
			{Name: "Planner", Index: 1, State: TabStateDone, StartedAt: time.Now().Add(-1 * time.Minute), ExitCode: 0},
			{Name: "Worker #1", Index: 2, State: TabStateRunning, StartedAt: time.Now().Add(-30 * time.Second), Attention: true},
		},
		ActiveTab: 2,
		Logs: []LogEntry{
			{Time: time.Now().Add(-10 * time.Second), Level: "INFO", Message: "Worker #1 started"},
			{Time: time.Now().Add(-5 * time.Second), Level: "WARN", Message: "BEL detected"},
		},
		Width:  80,
		Height: 24,
	}
}

func TestModel_Init(t *testing.T) {
	m := NewModel(testSnapshot())
	assert.Equal(t, 2, m.cursor, "cursor should start on active tab")
	assert.True(t, m.showLogs)
}

func TestModel_Quit(t *testing.T) {
	m := NewModel(testSnapshot())
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	model := updated.(Model)
	assert.True(t, model.result.Quit)
	assert.NotNil(t, cmd)
}

func TestModel_EnterResumes(t *testing.T) {
	m := NewModel(testSnapshot())
	// Move cursor to tab 1.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = updated.(Model)
	assert.Equal(t, 0, m.cursor)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model := updated.(Model)
	assert.Equal(t, 0, model.result.NewActive)
	assert.False(t, model.result.Quit)
	assert.NotNil(t, cmd)
}

func TestModel_NumberKeySwitches(t *testing.T) {
	m := NewModel(testSnapshot())
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	model := updated.(Model)
	assert.Equal(t, 0, model.result.NewActive)
	assert.NotNil(t, cmd)
}

func TestModel_ToggleLogs(t *testing.T) {
	m := NewModel(testSnapshot())
	assert.True(t, m.showLogs)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	model := updated.(Model)
	assert.False(t, model.showLogs)
}

func TestModel_View_NotEmpty(t *testing.T) {
	m := NewModel(testSnapshot())
	view := m.View()
	assert.Contains(t, view, "Orqestra")
	assert.Contains(t, view, "Worker")
	assert.Contains(t, view, "Intake")
}

func TestModel_Navigation(t *testing.T) {
	m := NewModel(testSnapshot())
	assert.Equal(t, 2, m.cursor)

	// Can't go down (already at last).
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(Model)
	assert.Equal(t, 2, m.cursor)

	// Go up.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = updated.(Model)
	assert.Equal(t, 1, m.cursor)
}
