package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/orchestrator"
	"github.com/xiii/orqestra/internal/tui/keymap"
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

// PlanChatMode handles the plan review gate (GateAfterDeliberation).
// The plan itself is shown in the Timeline as a Plan Frame; this mode provides
// only the key-driven approve/cancel/comment interaction.
type PlanChatMode struct {
	chatHistory []ChatEntry
	pending     tea.Msg
	keys        keymap.Bindings
}

// SimpleChatMode handles non-plan gates that present a plain chat prompt.
type SimpleChatMode struct {
	chatHistory []ChatEntry
	pending     tea.Msg
	keys        keymap.Bindings
}

// newHumanChatMode creates a HumanChatMode for the given gate request.
func newHumanChatMode(req orchestrator.GateRequest, keys keymap.Bindings) HumanChatMode {
	if req.Position.IsPlanGate() {
		return &PlanChatMode{keys: keys}
	}
	return &SimpleChatMode{keys: keys}
}

// SetSize is a no-op for PlanChatMode (plan is rendered by Timeline).
func (m *PlanChatMode) SetSize(_, _ int) {}

// Update handles messages for PlanChatMode.
// Approve and Cancel are intercepted for the hard gate.
func (m *PlanChatMode) Update(msg tea.Msg) (HumanChatMode, tea.Cmd) {
	if km, ok := msg.(tea.KeyPressMsg); ok {
		switch {
		case key.Matches(km, m.keys.ApprovePlan):
			m.pending = &orchestrator.Decision{Type: orchestrator.DecisionApprove}
		case key.Matches(km, m.keys.Cancel):
			m.pending = &orchestrator.Decision{Type: orchestrator.DecisionCancel}
		}
	}
	return m, nil
}

// View renders any accumulated chat history above the plan.
// The plan itself is visible in the Timeline as a Plan Frame.
func (m *PlanChatMode) View(width int) string {
	var b strings.Builder
	if len(m.chatHistory) > 0 {
		for _, entry := range m.chatHistory {
			b.WriteString(fmt.Sprintf("## %s\n%s\n\n", entry.Role, entry.Text))
		}
	}
	return b.String()
}

// Footer returns the footer hint for PlanChatMode.
func (m *PlanChatMode) Footer() string {
	return "[^A] approve  [^E] edit  [^C] abort  [↑↓/PgUp/PgDn] scroll"
}

// Pending returns the pending decision for PlanChatMode.
func (m *PlanChatMode) Pending() tea.Msg { return m.pending }

// SetSize is a no-op for SimpleChatMode (no sub-model with dimensions).
func (m *SimpleChatMode) SetSize(_, _ int) {}

// Update handles messages for SimpleChatMode.
func (m *SimpleChatMode) Update(msg tea.Msg) (HumanChatMode, tea.Cmd) {
	if km, ok := msg.(tea.KeyPressMsg); ok {
		switch {
		case key.Matches(km, m.keys.Submit):
			m.pending = &orchestrator.Decision{
				Type:    orchestrator.DecisionComment,
				Comment: "user comment",
			}
		case key.Matches(km, m.keys.ApprovePlan):
			m.pending = &orchestrator.Decision{Type: orchestrator.DecisionApprove}
		case key.Matches(km, m.keys.Cancel):
			m.pending = &orchestrator.Decision{Type: orchestrator.DecisionCancel}
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
