package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// tabsView manages a tabbed container for interactive PTY agent sessions.
type tabsView struct {
	termTabs  []termView
	tabNames  []string
	attention []bool // per-tab attention marker (BEL detected)
	active    int
	width     int
	height    int
	focused   bool

	pulseFrame int
	pulsing    bool
}

func newTabsView() tabsView {
	return tabsView{}
}

// AddTermTab creates a new PTY terminal tab with the given name.
func (t *tabsView) AddTermTab(name string) int {
	idx := len(t.tabNames)
	cols := t.width
	if cols < 1 {
		cols = 80
	}
	rows := t.height - 3
	if rows < 3 {
		rows = 24
	}
	tv := newTermView(idx, cols, rows)
	t.termTabs = append(t.termTabs, tv)
	t.tabNames = append(t.tabNames, name)
	t.attention = append(t.attention, false)
	return idx
}

// TermTab returns a pointer to the termView at the given tab index.
func (t *tabsView) TermTab(idx int) *termView {
	if idx < 0 || idx >= len(t.termTabs) {
		return nil
	}
	return &t.termTabs[idx]
}

// SetAttention marks a tab as needing user attention.
func (t *tabsView) SetAttention(idx int) {
	if idx >= 0 && idx < len(t.attention) {
		t.attention[idx] = true
	}
}

// ClearAttention removes the attention marker from a tab.
func (t *tabsView) ClearAttention(idx int) {
	if idx >= 0 && idx < len(t.attention) {
		t.attention[idx] = false
	}
}

// FirstAttentionTab returns the index of the first tab with an attention marker, or -1.
func (t *tabsView) FirstAttentionTab() int {
	for i, a := range t.attention {
		if a {
			return i
		}
	}
	return -1
}

func (t tabsView) Update(msg tea.Msg) (tabsView, tea.Cmd) {
	switch msg := msg.(type) {
	case PulseTickMsg:
		if !t.hasRunningTabs() {
			t.pulsing = false
			return t, nil
		}
		t.pulseFrame = (t.pulseFrame + 1) % len(pulseFrames)
		return t, pulseTickCmd()

	case tea.WindowSizeMsg:
		t.width = msg.Width - 2
		t.height = msg.Height
		termMsg := tea.WindowSizeMsg{Width: t.width - 2, Height: t.height - 2 - 2 - 1}
		for i := range t.termTabs {
			t.termTabs[i], _ = t.termTabs[i].Update(termMsg)
		}
		return t, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "alt+1", "alt+2", "alt+3", "alt+4", "alt+5", "alt+6", "alt+7", "alt+8", "alt+9":
			idx := int(msg.String()[len(msg.String())-1] - '1')
			if idx >= 0 && idx < len(t.tabNames) {
				t.active = idx
				t.ClearAttention(idx)
			}
			return t, nil
		}

	case PTYOutputMsg:
		if msg.TabIndex >= 0 && msg.TabIndex < len(t.termTabs) {
			var cmd tea.Cmd
			t.termTabs[msg.TabIndex], cmd = t.termTabs[msg.TabIndex].Update(msg)
			return t, cmd
		}
		return t, nil

	case PTYDoneMsg:
		if msg.TabIndex >= 0 && msg.TabIndex < len(t.termTabs) {
			var cmd tea.Cmd
			t.termTabs[msg.TabIndex], cmd = t.termTabs[msg.TabIndex].Update(msg)
			return t, cmd
		}
		return t, nil
	}

	// Forward to active tab
	if len(t.termTabs) > 0 && t.active < len(t.termTabs) {
		var cmd tea.Cmd
		t.termTabs[t.active], cmd = t.termTabs[t.active].Update(msg)
		return t, cmd
	}

	return t, nil
}

func (t tabsView) View() string {
	if len(t.tabNames) == 0 {
		return ""
	}

	// Render tab bar
	var tabs []string
	for i, name := range t.tabNames {
		displayName := name

		if i < len(t.attention) && t.attention[i] {
			displayName = "⚠ " + name
		} else if i < len(t.termTabs) && t.termTabs[i].done {
			displayName = "✓ " + name
		} else if t.pulsing {
			displayName = pulseFrames[t.pulseFrame] + " " + name
		}

		if i == t.active {
			tabs = append(tabs, activeTabStyle.Render(displayName))
		} else if i < len(t.attention) && t.attention[i] {
			tabs = append(tabs, attentionTabStyle.Render(displayName))
		} else {
			tabs = append(tabs, inactiveTabStyle.Render(displayName))
		}
	}

	tabBar := lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
	gap := tabGapStyle.Render(strings.Repeat(" ", max(0, t.width-lipgloss.Width(tabBar))))
	tabRow := lipgloss.JoinHorizontal(lipgloss.Bottom, tabBar, gap)

	// Render active tab content
	var content string
	if t.active < len(t.termTabs) {
		content = t.termTabs[t.active].View()
	}

	border := tabContentStyle
	if t.focused {
		border = tabContentFocusedStyle
	}
	wrappedContent := border.Width(t.width).Render(content)

	return tabRow + "\n" + wrappedContent
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

var pulseFrames = []string{"✦", "★", "✦", "✧", "·", "✧"}

const pulseInterval = 200 * time.Millisecond

func (t tabsView) hasRunningTabs() bool {
	for _, tab := range t.termTabs {
		if !tab.done {
			return true
		}
	}
	return false
}

func pulseTickCmd() tea.Cmd {
	return tea.Tick(pulseInterval, func(_ time.Time) tea.Msg {
		return PulseTickMsg{}
	})
}
