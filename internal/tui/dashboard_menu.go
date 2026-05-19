package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// AgentMenuModel manages the left pane agent list in the dashboard.
type AgentMenuModel struct {
	items   []AgentCard
	cursor  int
	width   int
	height  int
	focused bool

	PendingIntent tea.Msg
}

// NewAgentMenuModel creates an empty menu.
func NewAgentMenuModel() AgentMenuModel {
	return AgentMenuModel{focused: true}
}

// SetSize updates dimensions.
func (m *AgentMenuModel) SetSize(w, h int) {
	m.width = w
	m.height = h
}

// SetAgents updates the agent list from current pipeline state.
func (m *AgentMenuModel) SetAgents(agents []AgentRow) {
	m.items = make([]AgentCard, len(agents))
	for i, a := range agents {
		m.items[i] = agentRowToCard(a)
	}
	if m.cursor >= len(m.items) {
		m.cursor = max(0, len(m.items)-1)
	}
}

// SelectedID returns the ID of the currently selected agent.
func (m AgentMenuModel) SelectedID() string {
	if m.cursor < len(m.items) {
		return m.items[m.cursor].ID
	}
	return ""
}

// Update handles key events when focused.
func (m AgentMenuModel) Update(msg tea.Msg) (AgentMenuModel, tea.Cmd) {
	m.PendingIntent = nil
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}

	prevCursor := m.cursor
	switch key.Code {
	case tea.KeyUp:
		if m.cursor > 0 {
			m.cursor--
		}
	case tea.KeyDown:
		if m.cursor < len(m.items)-1 {
			m.cursor++
		}
	case tea.KeyPgUp:
		m.cursor = max(0, m.cursor-5)
	case tea.KeyPgDown:
		m.cursor = min(len(m.items)-1, m.cursor+5)
	}

	if m.cursor != prevCursor {
		m.PendingIntent = agentSelectedMsg{id: m.items[m.cursor].ID}
	}
	return m, nil
}

// View renders the agent menu cards.
func (m AgentMenuModel) View() string {
	if len(m.items) == 0 {
		return " No agents"
	}

	var b strings.Builder
	for i, card := range m.items {
		selected := i == m.cursor

		icon := statusIconForState(card.State)
		name := card.ID
		elapsed := formatDuration(card.Elapsed)

		// Card header
		header := fmt.Sprintf("%s %s", icon, name)
		if elapsed != "" {
			header += " " + elapsed
		}

		// Card body
		var body string
		if card.ModelDisplay != "" {
			body = fmt.Sprintf("  %s", card.ModelDisplay)
			if card.InputTokens > 0 || card.OutputTokens > 0 {
				body += fmt.Sprintf(" ↑%s ↓%s", formatTokenCompact(card.InputTokens), formatTokenCompact(card.OutputTokens))
			}
		}

		// Render with highlight if selected
		style := lipgloss.NewStyle()
		if selected && m.focused {
			style = style.Bold(true).Foreground(lipgloss.Color("12"))
		}

		b.WriteString(style.Render(header))
		b.WriteString("\n")
		if body != "" {
			b.WriteString(style.Render(body))
			b.WriteString("\n")
		}
	}
	return b.String()
}

func statusIconForState(state AgentState) string {
	switch state {
	case AgentStateDone:
		return "✓"
	case AgentStateFailed:
		return "✗"
	case AgentStateCancelled:
		return "⊘"
	case AgentStateGate:
		return "●"
	case AgentStateRunning:
		return "▶"
	default:
		return "○"
	}
}
