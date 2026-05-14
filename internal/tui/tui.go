package tui

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
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

// RunHeadlessPlanOnly runs the pipeline through planning (researcher → architect
// → critic) then stops without executing the worker. Returns the final plan and
// run directory so callers can inspect artifacts.
func RunHeadlessPlanOnly(ctx context.Context, engine *orchestrator.Engine, prompt string) (orchestrator.Result, error) {
	channels := engine.Start(ctx, orchestrator.Input{
		Prompt:      prompt,
		AutoApprove: true,
		NoExecute:   true,
	})

	var result orchestrator.Result
	for event := range channels.Events {
		if event.Type == orchestrator.EventError && event.Err != nil {
			return orchestrator.Result{}, event.Err
		}
		if event.Type == orchestrator.EventComplete {
			result = orchestrator.Result{
				Status:    event.Status,
				FinalPlan: event.FinalPlan,
				RunDir:    event.RunDir,
			}
		}
	}
	return result, nil
}
