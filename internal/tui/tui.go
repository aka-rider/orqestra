package tui

import (
	"context"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// Run starts the full-screen Bubble Tea TUI.
func Run(engine *orchestrator.Engine) error {
	model := NewModel(engine)
	p := tea.NewProgram(model, tea.WithAltScreen())

	defer func() {
		if r := recover(); r != nil {
			p.Kill()
			fmt.Fprintf(os.Stderr, "TUI panic recovered: %v\n", r)
		}
	}()

	_, err := p.Run()
	return err
}

// RunHeadless runs the pipeline non-interactively with auto-approve on all gates.
func RunHeadless(ctx context.Context, engine *orchestrator.Engine, prompt string) error {
	channels := engine.Start(ctx, orchestrator.Input{
		Prompt:      prompt,
		AutoApprove: true,
	})

	for event := range channels.Events {
		if event.Type == orchestrator.EventError && event.Err != nil {
			return event.Err
		}
	}
	return nil
}
