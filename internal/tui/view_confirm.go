package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// confirmView renders the confirmation prompt and captures y/N input.
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
		case "y", "Y":
			c.decided = true
			return c, func() tea.Msg { return ConfirmMsg{Approved: true} }
		case "n", "N", "enter":
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
	return confirmStyle.Render("Approve this plan? [y/N]: ")
}
