package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/orchestrator"

	"rune/pkg/ui/components/markdownedit"
	"rune/pkg/ui/components/textedit"
)

// HumanChatMode is the interface for human-in-the-loop chat modes during gates.
type HumanChatMode interface {
	// Update handles user input and returns a new mode and optional command.
	Update(msg tea.Msg) (HumanChatMode, tea.Cmd)
	// View renders the chat mode content.
	View(width int) string
	// Footer returns the footer hint text.
	Footer() string
	// Pending returns a non-nil message when the user has acted (e.g., approved, cancelled, commented).
	// The parent model drains this in Update() and sends it to the decisions channel.
	Pending() tea.Msg
	// SetSize propagates the allocated body dimensions to sub-models.
	SetSize(w, h int)
}

// PlanChatMode handles plan review gates (GateAfterResearch, GateAfterDeliberation).
type PlanChatMode struct {
	view        markdownedit.Model
	chatHistory []ChatEntry
	pending     tea.Msg
}

// SimpleChatMode handles non-plan gates that present a plain chat prompt.
type SimpleChatMode struct {
	chatHistory []ChatEntry
	pending     tea.Msg
}

// newHumanChatMode creates a HumanChatMode for the given gate request.
func newHumanChatMode(req orchestrator.GateRequest, ui runeUI) HumanChatMode {
	if req.Position.IsPlanGate() {
		view := markdownedit.New(ui.keys, ui.styles, ui.caps,
			markdownedit.WithRegistry(ui.registry),
			markdownedit.WithResolver(ui.resolver),
		)
		view = view.SetReadOnly(true)
		view = view.SetFocused(false)
		if req.PlanFilePath != "" {
			view = view.SetDir(req.PlanFilePath)
		}
		view = view.SetContent(req.FinalPlanMarkdown)
		return &PlanChatMode{
			view:        view,
			chatHistory: []ChatEntry{},
		}
	}
	return &SimpleChatMode{
		chatHistory: []ChatEntry{},
	}
}

// SetSize sets the allocated display area for the markdownedit view.
func (m *PlanChatMode) SetSize(w, h int) {
	m.view = m.view.SetRect(textedit.Rect{W: w, H: h})
}

// Update handles messages for PlanChatMode.
// Ctrl+A and Ctrl+C are intercepted for approve/cancel; everything else
// (including scroll keys) is forwarded to the markdownedit view.
func (m *PlanChatMode) Update(msg tea.Msg) (HumanChatMode, tea.Cmd) {
	if km, ok := msg.(tea.KeyPressMsg); ok {
		switch km.String() {
		case "ctrl+a":
			m.pending = &orchestrator.Decision{Type: orchestrator.DecisionApprove}
			return m, nil
		case "ctrl+c":
			m.pending = &orchestrator.Decision{Type: orchestrator.DecisionCancel}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.view, cmd = m.view.Update(msg)
	return m, cmd
}

// View renders the PlanChatMode using the markdownedit component.
func (m *PlanChatMode) View(width int) string {
	var b strings.Builder
	b.WriteString(m.view.View())
	if len(m.chatHistory) > 0 {
		b.WriteString("\n")
		for _, entry := range m.chatHistory {
			b.WriteString(fmt.Sprintf("## %s\n%s\n\n", entry.Role, entry.Text))
		}
	}
	return b.String()
}

// Footer returns the footer hint for PlanChatMode.
func (m *PlanChatMode) Footer() string {
	return "[^A] approve  [^C] abort  [↑↓/PgUp/PgDn] scroll"
}

// Pending returns the pending decision for PlanChatMode.
func (m *PlanChatMode) Pending() tea.Msg { return m.pending }

// SetSize is a no-op for SimpleChatMode (no sub-model with dimensions).
func (m *SimpleChatMode) SetSize(w, h int) {}

// Update handles messages for SimpleChatMode.
func (m *SimpleChatMode) Update(msg tea.Msg) (HumanChatMode, tea.Cmd) {
	if km, ok := msg.(tea.KeyPressMsg); ok {
		switch km.String() {
		case "enter", "ctrl+j":
			m.pending = &orchestrator.Decision{
				Type:    orchestrator.DecisionComment,
				Comment: "user comment",
			}
			return m, nil
		case "ctrl+a":
			m.pending = &orchestrator.Decision{Type: orchestrator.DecisionApprove}
			return m, nil
		case "ctrl+c":
			m.pending = &orchestrator.Decision{Type: orchestrator.DecisionCancel}
			return m, nil
		}
	}
	return m, nil
}

// View renders the SimpleChatMode.
func (m *SimpleChatMode) View(width int) string {
	var b strings.Builder
	for _, entry := range m.chatHistory {
		b.WriteString(fmt.Sprintf("## %s\n%s\n\n", entry.Role, entry.Text))
	}
	return b.String()
}

// Footer returns the footer hint for SimpleChatMode.
func (m *SimpleChatMode) Footer() string {
	return "[Enter] send  [^A] approve  [^C] abort"
}

// Pending returns the pending decision for SimpleChatMode.
func (m *SimpleChatMode) Pending() tea.Msg { return m.pending }
