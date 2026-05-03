package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/xiii/orqestra/internal/types"
)

const cursorBlinkInterval = 750 * time.Millisecond

// confirmView renders the plan in a scrollable bordered viewport with a
// separate bordered input pane below it for the y/N decision.
type confirmView struct {
	decided       bool
	cursorVisible bool
	planFocused   bool
	viewport      viewport.Model
	ready         bool
	termWidth     int
	termHeight    int
	planText      string
}

func newConfirmView() confirmView {
	return confirmView{cursorVisible: true, planFocused: true}
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

// Blur clears cursor visibility. The blink loop stops naturally once the
// parent stops forwarding CursorBlinkMsg.
func (cv *confirmView) Blur() {
	cv.cursorVisible = false
}

// SetPlanText sets the plan content displayed in the scrollable viewport.
// If the viewport is already initialised, content is updated immediately.
func (cv *confirmView) SetPlanText(text string) {
	cv.planText = text
	if cv.ready {
		cv.viewport.SetContent(text)
	}
}

func (cv confirmView) Update(msg tea.Msg) (confirmView, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		cv.termWidth = msg.Width
		cv.termHeight = msg.Height
		planHeight := msg.Height - 7 // reserve 5 lines for input pane + 2 border lines
		if planHeight < 3 {
			planHeight = 3
		}
		vp := viewport.New(msg.Width-2, planHeight)
		vp.SetContent(cv.planText)
		cv.viewport = vp
		cv.ready = true
		return cv, nil

	case CursorBlinkMsg:
		cv.cursorVisible = !cv.cursorVisible
		return cv, blinkCmd()

	case tea.MouseMsg:
		if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
			if cv.ready {
				var cmd tea.Cmd
				cv.viewport, cmd = cv.viewport.Update(msg)
				return cv, cmd
			}
		}
		return cv, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "up", "down", "pgup", "pgdown", "home", "end":
			if cv.planFocused && cv.ready {
				var cmd tea.Cmd
				cv.viewport, cmd = cv.viewport.Update(msg)
				return cv, cmd
			}
			return cv, nil

		case "a", "A", "y", "Y":
			cv.decided = true
			return cv, func() tea.Msg { return ConfirmMsg{Choice: ConfirmAccept} }

		case "r", "R", "n", "N":
			cv.decided = true
			return cv, func() tea.Msg { return ConfirmMsg{Choice: ConfirmReject} }

		case "e", "E":
			cv.decided = true
			return cv, func() tea.Msg { return ConfirmMsg{Choice: ConfirmEdit} }
		}
	}
	return cv, nil
}

func (cv confirmView) View() string {
	if !cv.ready {
		return "Loading..."
	}

	// Plan pane
	planBorder := stylePlanPane
	if cv.planFocused {
		planBorder = stylePlanPaneFocused
	}

	total := cv.viewport.TotalLineCount()
	if total < 1 {
		total = 1
	}
	var scrollHint string
	if cv.planFocused {
		scrollHint = "↑↓ scroll  tab→prompt  line %d/%d"
	} else {
		scrollHint = "tab→plan  line %d/%d"
	}
	scrollInfo := dimStyle.Render(fmt.Sprintf(scrollHint, cv.viewport.YOffset+1, total))

	planSection := lipgloss.JoinVertical(lipgloss.Left,
		scrollInfo,
		planBorder.Width(cv.termWidth-2).Render(cv.viewport.View()),
	)

	// Input pane
	inputBorder := InputBoxStyle

	approve := approveKeyStyle.Render("[A]")
	reject := rejectKeyStyle.Render("[R]")
	edit := editKeyStyle.Render("[E]")
	cursor := " "
	if cv.cursorVisible {
		cursor = "▌"
	}
	prompt := confirmStyle.Render("Approve this plan? ") + approve + "pprove / " + reject + "eject / " + edit + "dit " + cursor
	hint := dimStyle.Render("(a/y) approve  (r/n) reject  (e) edit  tab to scroll plan")
	inputContent := lipgloss.JoinVertical(lipgloss.Left, prompt, hint)
	inputSection := inputBorder.Width(cv.termWidth - 2).Render(inputContent)

	return lipgloss.JoinVertical(lipgloss.Left, planSection, inputSection)
}

// renderSpecText renders a Specification to a lipgloss-styled string
// suitable for display inside the plan viewport.
func renderSpecText(spec types.Specification) string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("EXECUTION PLAN"))
	b.WriteString("\n\n")
	b.WriteString(goalStyle.Render("Goal: " + spec.Goal))
	b.WriteString("\n\n")

	if len(spec.Steps) > 0 {
		b.WriteString(titleStyle.Render("Steps:"))
		b.WriteString("\n")
		for i, step := range spec.Steps {
			b.WriteString(stepStyle.Render(fmt.Sprintf("%d. %s", i+1, step)))
			b.WriteString("\n")
		}
	}

	if len(spec.Acceptance) > 0 {
		b.WriteString("\n")
		b.WriteString(titleStyle.Render("Acceptance Criteria:"))
		b.WriteString("\n")
		for i, criterion := range spec.Acceptance {
			b.WriteString(acceptanceStyle.Render(fmt.Sprintf("✓ %d. %s", i+1, criterion)))
			b.WriteString("\n")
		}
	}

	return b.String()
}
