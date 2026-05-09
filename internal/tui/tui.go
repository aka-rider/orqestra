package tui

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// Run starts the full-screen Bubble Tea TUI.
func Run(engine *orchestrator.Engine, configName string) error {
	// Set up file-based debug logging (survives TUI mode which silences slog)
	logDir := filepath.Join(os.Getenv("HOME"), ".orqestra")
	if err := os.MkdirAll(logDir, 0o755); err == nil {
		if f, err := os.OpenFile(filepath.Join(logDir, "tui.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
			slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})))
		}
	}

	model := NewModel(engine, configName)
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())

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
