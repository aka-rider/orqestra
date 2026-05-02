package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// savedView is shown after the user presses 'e' to save the plan for offline editing.
type savedView struct {
	filePath string
	width    int
	height   int
}

func newSavedView(filePath string) savedView {
	return savedView{filePath: filePath}
}

func (sv savedView) Update(msg tea.Msg) (savedView, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		sv.width = msg.Width
		sv.height = msg.Height
	case tea.KeyMsg:
		// ctrl+c is handled globally; any other key exits cleanly.
		_ = msg
		return sv, tea.Quit
	}
	return sv, nil
}

func (sv savedView) View() string {
	line1 := goalStyle.Render("Plan saved to " + sv.filePath)
	line2 := dimStyle.Render("Edit it, then run:  orqestra --plan " + sv.filePath)
	hint := dimStyle.Render("(press any key to exit)")
	content := lipgloss.JoinVertical(lipgloss.Left, line1, line2, "", hint)
	return lipgloss.NewStyle().Padding(1, 2).Render(content)
}
