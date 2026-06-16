package tui

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// Run starts the full-screen Bubble Tea TUI.
func Run(engine *orchestrator.Engine, configName string) error {
	model := NewModel(engine, configName)
	p := tea.NewProgram(model)

	defer func() {
		if r := recover(); r != nil {
			p.Kill()
			fmt.Fprintf(os.Stderr, "TUI panic recovered: %v\n", r)
		}
	}()

	_, err := p.Run()
	return err
}
