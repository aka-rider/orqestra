package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// confirmView renders the confirmation prompt and captures A/R input.
type confirmView struct {
	decided bool
}

func newConfirmView() confirmView {
	return confirmView{}
}

func (c confirmView) Update(msg tea.Msg) (confirmView, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "a", "A", "y", "Y":
			c.decided = true
			return c, func() tea.Msg { return ConfirmMsg{Approved: true} }
		case "r", "R", "n", "N":
			c.decided = true
			return c, func() tea.Msg { return ConfirmMsg{Approved: false} }
		}
	}
	return c, nil
}

func (c confirmView) View() string {
	if c.decided {
		return ""
	}
	approve := approveKeyStyle.Render("[A]")
	reject := rejectKeyStyle.Render("[R]")
	return confirmStyle.Render("Approve this plan? ") + approve + "pprove / " + reject + "eject"
}
