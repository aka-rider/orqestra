package tui

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// Run starts the full-screen Bubble Tea TUI.
//
// QuestionBridge lifecycle (WP4b/J5,J41): the bridge is started exactly ONCE
// here, on a context scoped to this whole TUI session — never per pipeline
// run. Run is the sole current entry point that hands the Engine to the TUI,
// so its lifetime already IS the bridge's intended lifetime; owning it here
// needs no new parameter on Run's signature and no change at the cmd/orqestra
// call site (the simplest correct owner). Engine.startNew only ever forwards
// questions from the bridge onto each run's own event bus (RunEvent) — it
// never starts or stops the bridge itself (see the lifecycle comment on
// startNew). A future
// headless entry point (WP16) would own its own bridge lifecycle the same
// way, at its own place, since it won't call tui.Run.
func Run(engine *orchestrator.Engine, configName string) error {
	model, err := NewModel(engine, configName)
	if err != nil {
		return fmt.Errorf("init TUI: %w", err)
	}

	if engine.QuestionBridge != nil {
		bridgeCtx, bridgeCancel := context.WithCancel(context.Background())
		defer bridgeCancel()
		go func() {
			if bridgeErr := engine.QuestionBridge.Run(bridgeCtx); bridgeErr != nil {
				slog.Warn("question bridge", "err", bridgeErr)
			}
		}()
	}

	p := tea.NewProgram(model)

	defer func() {
		if r := recover(); r != nil {
			p.Kill()
			fmt.Fprintf(os.Stderr, "TUI panic recovered: %v\n", r)
		}
	}()

	_, runErr := p.Run()
	return runErr
}
