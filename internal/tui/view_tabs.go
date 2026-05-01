package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// tabsView manages a tabbed container for multiple harness streaming sessions.
type tabsView struct {
	tabs     []streamView
	tabNames []string
	active   int
	width    int
	height   int
}

func newTabsView() tabsView {
	return tabsView{}
}

// AddTab creates a new tab with the given name.
func (t *tabsView) AddTab(name string) int {
	idx := len(t.tabs)
	sv := newStreamView(idx)
	if t.width > 0 {
		sv.SetSize(t.width, t.height-3) // account for tab bar
	}
	t.tabs = append(t.tabs, sv)
	t.tabNames = append(t.tabNames, name)
	return idx
}

func (t tabsView) Update(msg tea.Msg) (tabsView, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		t.width = msg.Width - 4
		t.height = msg.Height - 8
		for i := range t.tabs {
			t.tabs[i].SetSize(t.width, t.height-3)
		}
		return t, nil

	case TabSwitchMsg:
		if msg.Index >= 0 && msg.Index < len(t.tabs) {
			t.active = msg.Index
		}
		return t, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "alt+1", "alt+2", "alt+3", "alt+4", "alt+5", "alt+6", "alt+7", "alt+8", "alt+9":
			idx := int(msg.String()[len(msg.String())-1] - '1')
			if idx >= 0 && idx < len(t.tabs) {
				t.active = idx
			}
			return t, nil
		}

	case StreamChunkMsg:
		if msg.TabIndex >= 0 && msg.TabIndex < len(t.tabs) {
			var cmd tea.Cmd
			t.tabs[msg.TabIndex], cmd = t.tabs[msg.TabIndex].Update(msg)
			return t, cmd
		}
		return t, nil

	case HarnessDoneMsg:
		if msg.TabIndex >= 0 && msg.TabIndex < len(t.tabs) {
			var cmd tea.Cmd
			t.tabs[msg.TabIndex], cmd = t.tabs[msg.TabIndex].Update(msg)
			return t, cmd
		}
		return t, nil
	}

	// Forward to active tab
	if len(t.tabs) > 0 && t.active < len(t.tabs) {
		var cmd tea.Cmd
		t.tabs[t.active], cmd = t.tabs[t.active].Update(msg)
		return t, cmd
	}

	return t, nil
}

func (t tabsView) View() string {
	if len(t.tabs) == 0 {
		return ""
	}

	// Render tab bar
	var tabs []string
	for i, name := range t.tabNames {
		if i == t.active {
			tabs = append(tabs, activeTabStyle.Render(name))
		} else {
			tabs = append(tabs, inactiveTabStyle.Render(name))
		}
	}

	tabBar := lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
	gap := tabGapStyle.Render(strings.Repeat(" ", max(0, t.width-lipgloss.Width(tabBar))))
	tabRow := lipgloss.JoinHorizontal(lipgloss.Bottom, tabBar, gap)

	// Render active tab content
	var content string
	if t.active < len(t.tabs) {
		content = t.tabs[t.active].View()
	}

	return tabRow + "\n" + content
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
