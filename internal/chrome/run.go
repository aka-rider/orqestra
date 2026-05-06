//go:build darwin

package chrome

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

// Run launches the chrome overlay as a momentary BubbleTea program with alt-screen.
// It blocks until the user exits chrome (picks a tab, resumes, or quits).
// The tty parameter should be the /dev/tty file opened by the mux. If nil, /dev/tty is opened.
func Run(tty *os.File, snap Snapshot) (Result, error) {
	m := NewModel(snap)

	var opts []tea.ProgramOption
	opts = append(opts, tea.WithAltScreen())
	if tty != nil {
		opts = append(opts, tea.WithInput(tty), tea.WithOutput(tty))
	}

	p := tea.NewProgram(m, opts...)

	finalModel, err := p.Run()
	if err != nil {
		return Result{Quit: true}, fmt.Errorf("chrome: %w", err)
	}

	if fm, ok := finalModel.(Model); ok {
		return fm.GetResult(), nil
	}

	return Result{NewActive: -1}, nil
}
