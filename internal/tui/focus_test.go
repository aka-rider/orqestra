package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestFocusCycling(t *testing.T) {
	m := NewModel(PipelineFuncs{})
	m.setState(StateConfirming)

	if m.focus != FocusPlan {
		t.Errorf("expected FocusPlan, got %v", m.focus)
	}

	res, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	m = res.(Model)
	if m.focus != FocusPrompt {
		t.Errorf("expected FocusPrompt, got %v", m.focus)
	}

	res, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	m = res.(Model)
	if m.focus != FocusPlan {
		t.Errorf("expected FocusPlan, got %v", m.focus)
	}

	res, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = res.(Model)
	if m.focus != FocusPrompt {
		t.Errorf("expected FocusPrompt, got %v", m.focus)
	}
}

func TestFocusTabAutocompleteGuard(t *testing.T) {
	m := NewModel(PipelineFuncs{})
	m.setState(StateIdle)
	m.commandBar.showAC = true
	m.focus = FocusPrompt

	res, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	m = res.(Model)
	if m.focus != FocusPrompt {
		t.Error("focus should not cycle when autocomplete is showing")
	}
}

func TestCommandBarAlwaysBordered(t *testing.T) {
	cb := newCommandBar(NewCommandRegistry())
	cb.SetWidth(80)
	cb.focused = true
	if !strings.Contains(cb.View(), "╭") {
		t.Errorf("expected rounded borders when focused, got:\n%s", cb.View())
	}
	cb.focused = false
	if !strings.Contains(cb.View(), "╭") {
		t.Error("expected rounded borders when unfocused")
	}
}

func TestPulseAnimationStartStop(t *testing.T) {
	tv := newTabsView()
	tv.AddTab("test")
	// tab is running by default

	tv, cmd := tv.Update(PulseTickMsg{})
	if tv.pulseFrame != 1 {
		t.Error("expected pulse frame 1")
	}
	if cmd == nil {
		t.Error("expected next tick cmd")
	}

	tv.tabs[0].done = true
	tv, cmd = tv.Update(PulseTickMsg{})
	if tv.pulsing {
		t.Error("should stop pulsing")
	}
	if cmd != nil {
		t.Error("should return nil cmd when stopping")
	}
}

func TestPulseFrameInTabName(t *testing.T) {
	tv := newTabsView()
	tv.AddTab("test")
	tv.pulsing = true
	tv.pulseFrame = 0

	out := tv.View()
	if !strings.Contains(out, pulseFrames[0]+" test") {
		t.Error("expected pulse frame in tab name")
	}
}

func TestSyncFocusOnStateChange(t *testing.T) {
	m := NewModel(PipelineFuncs{})
	m.setState(StatePlanning)
	m.syncFocus()

	if !m.tabsView.focused {
		t.Error("tabsView should be focused in StatePlanning")
	}
	if m.commandBar.focused {
		t.Error("commandBar should not be focused in StatePlanning")
	}

	m.setState(StateIdle)
	m.syncFocus()
	if m.tabsView.focused {
		t.Error("tabsView should not be focused in StateIdle")
	}
	if !m.commandBar.focused {
		t.Error("commandBar should be focused in StateIdle")
	}
}
