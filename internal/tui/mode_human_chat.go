package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/orchestrator"
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
}

// PlanChatMode handles plan review gates (GateAfterResearch, GateAfterDeliberation).
type PlanChatMode struct {
	plan         string
	planFilePath string
	chatHistory  []ChatEntry
	pending      tea.Msg
}

// SimpleChatMode handles non-plan gates that present a plain chat prompt.
type SimpleChatMode struct {
	chatHistory []ChatEntry
	pending     tea.Msg
}

// newHumanChatMode creates a HumanChatMode for the given gate request.
func newHumanChatMode(req orchestrator.GateRequest, width int) HumanChatMode {
	if req.Position.IsPlanGate() {
		return &PlanChatMode{
			plan:         req.FinalPlanMarkdown,
			planFilePath: req.PlanFilePath,
			chatHistory:  []ChatEntry{},
		}
	}
	return &SimpleChatMode{
		chatHistory: []ChatEntry{},
	}
}

// Update handles messages for PlanChatMode.
func (m *PlanChatMode) Update(msg tea.Msg) (HumanChatMode, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter", "ctrl+j":
			m.pending = &orchestrator.Decision{
				Type:    orchestrator.DecisionComment,
				Comment: "user comment",
			}
			return m, nil
		case "ctrl+a":
			m.pending = &orchestrator.Decision{
				Type: orchestrator.DecisionApprove,
			}
			return m, nil
		case "ctrl+c":
			m.pending = &orchestrator.Decision{
				Type: orchestrator.DecisionCancel,
			}
			return m, nil
		}
	}
	return m, nil
}

// View renders the PlanChatMode.
func (m *PlanChatMode) View(width int) string {
	var b strings.Builder
	b.WriteString("Plan:\n")
	b.WriteString(m.plan)
	b.WriteString("\n\n")
	for _, entry := range m.chatHistory {
		b.WriteString(fmt.Sprintf("## %s\n%s\n\n", entry.Role, entry.Text))
	}
	return b.String()
}

// Footer returns the footer hint for PlanChatMode.
func (m *PlanChatMode) Footer() string {
	return "[Enter] send  [^A] approve  [^C] abort"
}

// Pending returns the pending decision for PlanChatMode.
func (m *PlanChatMode) Pending() tea.Msg {
	return m.pending
}

// Update handles messages for SimpleChatMode.
func (m *SimpleChatMode) Update(msg tea.Msg) (HumanChatMode, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter", "ctrl+j":
			m.pending = &orchestrator.Decision{
				Type:    orchestrator.DecisionComment,
				Comment: "user comment",
			}
			return m, nil
		case "ctrl+a":
			m.pending = &orchestrator.Decision{
				Type: orchestrator.DecisionApprove,
			}
			return m, nil
		case "ctrl+c":
			m.pending = &orchestrator.Decision{
				Type: orchestrator.DecisionCancel,
			}
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
func (m *SimpleChatMode) Pending() tea.Msg {
	return m.pending
}
