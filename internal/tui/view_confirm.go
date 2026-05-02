package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const cursorBlinkInterval = 750 * time.Millisecond

// confirmView renders the confirmation prompt and captures A/R input.
type confirmView struct {
	decided       bool
	cursorVisible bool
}

func newConfirmView() confirmView {
	return confirmView{cursorVisible: true}
}

func blinkCmd() tea.Cmd {
	return tea.Tick(cursorBlinkInterval, func(time.Time) tea.Msg {
		return CursorBlinkMsg{}
	})
}

// Focus starts the cursor blink loop. Call when entering StateConfirming.
func (cv *confirmView) Focus() tea.Cmd {
	cv.cursorVisible = true
	return blinkCmd()
}

// Blur clears cursor visibility. The blink loop stops naturally: once the
// parent stops forwarding CursorBlinkMsg (on leaving StateConfirming), the
// last in-flight tick fires, is dropped, and blinkCmd is never re-armed.
func (cv *confirmView) Blur() {
	cv.cursorVisible = false
}

func (cv confirmView) Update(msg tea.Msg) (confirmView, tea.Cmd) {
	switch msg := msg.(type) {
	case CursorBlinkMsg:
		cv.cursorVisible = !cv.cursorVisible
		return cv, blinkCmd()
	case tea.KeyMsg:
		switch msg.String() {
		case "a", "A", "y", "Y":
			cv.decided = true
			return cv, func() tea.Msg { return ConfirmMsg{Approved: true} }
		case "r", "R", "n", "N":
			cv.decided = true
			return cv, func() tea.Msg { return ConfirmMsg{Approved: false} }
		}
	}
	return cv, nil
}

func (cv confirmView) View() string {
	if cv.decided {
		return ""
	}
	approve := approveKeyStyle.Render("[A]")
	reject := rejectKeyStyle.Render("[R]")
	cursor := " "
	if cv.cursorVisible {
		cursor = "▌"
	}
	return confirmStyle.Render("Approve this plan? ") + approve + "pprove / " + reject + "eject " + cursor
}
