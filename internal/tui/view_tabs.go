package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// tabKind identifies the type of content a tab renders.
type tabKind int

const (
	tabKindStream tabKind = iota
	tabKindTerm
)

// tabsView manages a tabbed container for multiple harness streaming sessions.
type tabsView struct {
	tabs       []streamView
	termTabs   []termView
	tabNames   []string
	tabKinds   []tabKind
	attention  []bool // per-tab attention marker (BEL detected)
	active     int
	width      int
	height     int
	focused    bool
	pulseFrame int
	pulsing    bool
}

func newTabsView() tabsView {
	return tabsView{}
}

// AddTab creates a new stream tab with the given name.
func (t *tabsView) AddTab(name string) int {
	idx := len(t.tabNames)
	sv := newStreamView(idx)
	if t.width > 0 {
		sv.SetSize(t.width, t.height-3) // account for tab bar
	}
	t.tabs = append(t.tabs, sv)
	t.tabNames = append(t.tabNames, name)
	t.tabKinds = append(t.tabKinds, tabKindStream)
	t.attention = append(t.attention, false)
	return idx
}

// AddTermTab creates a new PTY terminal tab with the given name.
// Returns the tab index. Use AttachPTY on the returned termView to wire input.
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
	t.tabKinds = append(t.tabKinds, tabKindTerm)
	t.attention = append(t.attention, false)
	return idx
}

// TermTab returns a pointer to the termView at the given tab index.
// Returns nil if the index is out of range or not a term tab.
func (t *tabsView) TermTab(idx int) *termView {
	if idx < 0 || idx >= len(t.tabKinds) || t.tabKinds[idx] != tabKindTerm {
		return nil
	}
	termIdx := t.termIndex(idx)
	if termIdx < 0 || termIdx >= len(t.termTabs) {
		return nil
	}
	return &t.termTabs[termIdx]
}

// termIndex converts a global tab index to the index within t.termTabs.
func (t *tabsView) termIndex(globalIdx int) int {
	count := 0
	for i := 0; i < globalIdx; i++ {
		if i < len(t.tabKinds) && t.tabKinds[i] == tabKindTerm {
			count++
		}
	}
	return count
}

// streamIndex converts a global tab index to the index within t.tabs (stream tabs).
func (t *tabsView) streamIndex(globalIdx int) int {
	count := 0
	for i := 0; i < globalIdx; i++ {
		if i < len(t.tabKinds) && t.tabKinds[i] == tabKindStream {
			count++
		}
	}
	return count
}

func (t tabsView) Update(msg tea.Msg) (tabsView, tea.Cmd) {
	switch msg := msg.(type) {
	case AttentionMsg:
		if msg.TabIndex >= 0 && msg.TabIndex < len(t.attention) {
			t.attention[msg.TabIndex] = true
		}
		return t, nil

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
		for i := range t.tabs {
			contentHeight := t.height - 2 - 2 - 1
			if contentHeight < 3 {
				contentHeight = 3
			}
			t.tabs[i].SetSize(t.width-2, contentHeight)
		}
		// Resize term tabs too.
		termMsg := tea.WindowSizeMsg{Width: t.width - 2, Height: t.height - 2 - 2 - 1}
		for i := range t.termTabs {
			t.termTabs[i], _ = t.termTabs[i].Update(termMsg)
		}
		return t, nil

	case TabSwitchMsg:
		if msg.Index >= 0 && msg.Index < len(t.tabNames) {
			t.active = msg.Index
			t.ClearAttention(msg.Index)
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

	case StreamChunkMsg:
		if msg.TabIndex >= 0 && msg.TabIndex < len(t.tabNames) && t.tabKinds[msg.TabIndex] == tabKindStream {
			sIdx := t.streamIndex(msg.TabIndex)
			if sIdx < len(t.tabs) {
				var cmd tea.Cmd
				t.tabs[sIdx], cmd = t.tabs[sIdx].Update(msg)
				return t, cmd
			}
		}
		return t, nil

	case HarnessDoneMsg:
		if msg.TabIndex >= 0 && msg.TabIndex < len(t.tabNames) && t.tabKinds[msg.TabIndex] == tabKindStream {
			sIdx := t.streamIndex(msg.TabIndex)
			if sIdx < len(t.tabs) {
				var cmd tea.Cmd
				t.tabs[sIdx], cmd = t.tabs[sIdx].Update(msg)
				return t, cmd
			}
		}
		return t, nil

	case PTYOutputMsg:
		if msg.TabIndex >= 0 && msg.TabIndex < len(t.tabNames) && t.tabKinds[msg.TabIndex] == tabKindTerm {
			tIdx := t.termIndex(msg.TabIndex)
			if tIdx < len(t.termTabs) {
				var cmd tea.Cmd
				t.termTabs[tIdx], cmd = t.termTabs[tIdx].Update(msg)
				return t, cmd
			}
		}
		return t, nil

	case PTYNeedsInputMsg:
		if msg.TabIndex >= 0 && msg.TabIndex < len(t.tabNames) && t.tabKinds[msg.TabIndex] == tabKindTerm {
			tIdx := t.termIndex(msg.TabIndex)
			if tIdx < len(t.termTabs) {
				var cmd tea.Cmd
				t.termTabs[tIdx], cmd = t.termTabs[tIdx].Update(msg)
				return t, cmd
			}
		}
		return t, nil

	case PTYDoneMsg:
		if msg.TabIndex >= 0 && msg.TabIndex < len(t.tabNames) && t.tabKinds[msg.TabIndex] == tabKindTerm {
			tIdx := t.termIndex(msg.TabIndex)
			if tIdx < len(t.termTabs) {
				var cmd tea.Cmd
				t.termTabs[tIdx], cmd = t.termTabs[tIdx].Update(msg)
				return t, cmd
			}
		}
		return t, nil
	}

	// Forward to active tab
	if len(t.tabNames) > 0 && t.active < len(t.tabNames) {
		if t.tabKinds[t.active] == tabKindStream {
			sIdx := t.streamIndex(t.active)
			if sIdx < len(t.tabs) {
				var cmd tea.Cmd
				t.tabs[sIdx], cmd = t.tabs[sIdx].Update(msg)
				return t, cmd
			}
		} else if t.tabKinds[t.active] == tabKindTerm {
			tIdx := t.termIndex(t.active)
			if tIdx < len(t.termTabs) {
				var cmd tea.Cmd
				t.termTabs[tIdx], cmd = t.termTabs[tIdx].Update(msg)
				return t, cmd
			}
		}
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

		// Attention marker takes precedence over other indicators.
		if i < len(t.attention) && t.attention[i] {
			displayName = "⚠ " + name
		} else if t.tabKinds[i] == tabKindStream {
			sIdx := t.streamIndex(i)
			if sIdx < len(t.tabs) && t.tabs[sIdx].done {
				displayName = "✓ " + name
			} else if t.pulsing {
				displayName = pulseFrames[t.pulseFrame] + " " + name
			}
		} else if t.tabKinds[i] == tabKindTerm {
			tIdx := t.termIndex(i)
			if tIdx < len(t.termTabs) && t.termTabs[tIdx].done {
				displayName = "✓ " + name
			} else if t.pulsing {
				displayName = pulseFrames[t.pulseFrame] + " " + name
			}
		}

		if i == t.active {
			tabs = append(tabs, activeTabStyle.Render(displayName))
		} else {
			if i < len(t.attention) && t.attention[i] {
				tabs = append(tabs, attentionTabStyle.Render(displayName))
			} else {
				tabs = append(tabs, inactiveTabStyle.Render(displayName))
			}
		}
	}

	tabBar := lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
	gap := tabGapStyle.Render(strings.Repeat(" ", max(0, t.width-lipgloss.Width(tabBar))))
	tabRow := lipgloss.JoinHorizontal(lipgloss.Bottom, tabBar, gap)

	// Render active tab content
	var content string
	if t.active < len(t.tabNames) {
		if t.tabKinds[t.active] == tabKindStream {
			sIdx := t.streamIndex(t.active)
			if sIdx < len(t.tabs) {
				content = t.tabs[sIdx].View()
			}
		} else if t.tabKinds[t.active] == tabKindTerm {
			tIdx := t.termIndex(t.active)
			if tIdx < len(t.termTabs) {
				content = t.termTabs[tIdx].View()
			}
		}
	}

	contentBorder := InputBoxStyle
	if t.focused {
		contentBorder = InputBoxFocusedStyle
	}
	wrappedContent := contentBorder.Width(t.width).Render(content)

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
	for _, tab := range t.tabs {
		if !tab.done {
			return true
		}
	}
	for _, tab := range t.termTabs {
		if !tab.done {
			return true
		}
	}
	return false
}

func pulseTickCmd() tea.Cmd {
	return tea.Tick(pulseInterval, func(time.Time) tea.Msg {
		return PulseTickMsg{}
	})
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

// FirstAttentionTab returns the index of the first tab with attention set, or -1.
func (t *tabsView) FirstAttentionTab() int {
	for i, a := range t.attention {
		if a {
			return i
		}
	}
	return -1
}
