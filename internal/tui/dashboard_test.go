package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestDashboard_InitialFocus(t *testing.T) {
	d := NewDashboardModel()
	if d.focus != FocusMenu {
		t.Errorf("initial focus = %d, want FocusMenu", d.focus)
	}
}

func TestDashboard_TabCycle(t *testing.T) {
	d := NewDashboardModel()
	d.SetSize(120, 40)
	d.SetAgents([]AgentRow{
		{ID: "researcher", State: "done"},
		{ID: "worker", State: "running"},
	})

	// Tab: Menu → ArtTop → ArtBottom → Log → Menu
	expected := []DashboardFocus{FocusArtTop, FocusArtBottom, FocusLog, FocusMenu}
	for _, want := range expected {
		d, _ = d.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		if d.focus != want {
			t.Errorf("after Tab: focus = %d, want %d", d.focus, want)
		}
	}
}

func TestDashboard_ShiftTabCycle(t *testing.T) {
	d := NewDashboardModel()
	d.SetSize(120, 40)
	d.SetAgents([]AgentRow{{ID: "worker", State: "running"}})

	// Shift+Tab: Menu → Log → ArtBottom → ArtTop → Menu
	expected := []DashboardFocus{FocusLog, FocusArtBottom, FocusArtTop, FocusMenu}
	for _, want := range expected {
		d, _ = d.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
		if d.focus != want {
			t.Errorf("after Shift+Tab: focus = %d, want %d", d.focus, want)
		}
	}
}

func TestDashboard_EscFromMenu_ClosesIntent(t *testing.T) {
	d := NewDashboardModel()
	d.SetSize(120, 40)

	d, _ = d.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if d.PendingIntent == nil {
		t.Fatal("expected CloseDashboardIntent")
	}
	if _, ok := d.PendingIntent.(CloseDashboardIntent); !ok {
		t.Errorf("PendingIntent = %T, want CloseDashboardIntent", d.PendingIntent)
	}
}

func TestDashboard_EscFromArtTop_ReturnsToMenu(t *testing.T) {
	d := NewDashboardModel()
	d.SetSize(120, 40)
	d.focus = FocusArtTop

	d, _ = d.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if d.focus != FocusMenu {
		t.Errorf("after Esc from ArtTop: focus = %d, want FocusMenu", d.focus)
	}
}

func TestDashboard_EscFromLog_ReturnsToMenu(t *testing.T) {
	d := NewDashboardModel()
	d.SetSize(120, 40)
	d.focus = FocusLog

	d, _ = d.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if d.focus != FocusMenu {
		t.Errorf("after Esc from Log: focus = %d, want FocusMenu", d.focus)
	}
}

func TestDashboard_EnterFromMenu_FocusesArtTop(t *testing.T) {
	d := NewDashboardModel()
	d.SetSize(120, 40)
	d.SetAgents([]AgentRow{{ID: "worker", State: "running"}})

	d, _ = d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if d.focus != FocusArtTop {
		t.Errorf("after Enter from Menu: focus = %d, want FocusArtTop", d.focus)
	}
}

func TestDashboard_MenuCursor(t *testing.T) {
	d := NewDashboardModel()
	d.SetSize(120, 40)
	d.SetAgents([]AgentRow{
		{ID: "researcher", State: "done"},
		{ID: "architect", State: "done"},
		{ID: "worker", State: "running"},
	})

	// Initial cursor at 0
	if d.menu.SelectedID() != "researcher" {
		t.Errorf("initial selected = %q, want researcher", d.menu.SelectedID())
	}

	// Down arrow
	d, _ = d.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if d.menu.SelectedID() != "architect" {
		t.Errorf("after Down: selected = %q, want architect", d.menu.SelectedID())
	}

	// Down again
	d, _ = d.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if d.menu.SelectedID() != "worker" {
		t.Errorf("after Down: selected = %q, want worker", d.menu.SelectedID())
	}

	// Down at end — should not move
	d, _ = d.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if d.menu.SelectedID() != "worker" {
		t.Errorf("after Down at end: selected = %q, want worker", d.menu.SelectedID())
	}

	// Up
	d, _ = d.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if d.menu.SelectedID() != "architect" {
		t.Errorf("after Up: selected = %q, want architect", d.menu.SelectedID())
	}
}

func TestDashboard_View_NonEmpty(t *testing.T) {
	d := NewDashboardModel()
	d.SetSize(120, 40)
	d.SetAgents([]AgentRow{
		{ID: "researcher", State: "done", Elapsed: 5 * time.Second, ModelDisplay: "claude-opus-4"},
		{ID: "worker", State: "running", StartedAt: time.Now()},
	})

	view := d.View()
	if view == "" {
		t.Error("expected non-empty dashboard view")
	}
	if len(view) < 20 {
		t.Errorf("dashboard view too short: %d chars", len(view))
	}
}

func TestDashboard_View_SmallTerminal(t *testing.T) {
	d := NewDashboardModel()
	d.SetSize(30, 3)

	view := d.View()
	if view == "" {
		t.Error("expected fallback message for small terminal")
	}
}

func TestAgentMenuModel_SetAgents(t *testing.T) {
	m := NewAgentMenuModel()
	m.SetAgents([]AgentRow{
		{ID: "a", State: "done"},
		{ID: "b", State: "running"},
	})
	if len(m.items) != 2 {
		t.Errorf("items = %d, want 2", len(m.items))
	}
	if m.items[0].ID != "a" || m.items[1].ID != "b" {
		t.Errorf("items IDs = [%s, %s], want [a, b]", m.items[0].ID, m.items[1].ID)
	}
}

func TestLogViewerModel_SetLines(t *testing.T) {
	l := NewLogViewerModel()
	l.SetSize(80, 10)
	l.SetLines([]string{"line1", "line2", "line3"}, false)

	view := l.View()
	if view == "" {
		t.Error("expected non-empty log view")
	}
}

func TestArtifactViewerModel_SetContent(t *testing.T) {
	a := NewArtifactViewerModel()
	a.SetSize(80, 20)
	a.SetContent("# Input\nSome prompt text", "# Output\nSome plan text")

	view := a.View()
	if view == "" {
		t.Error("expected non-empty artifact view")
	}
}
