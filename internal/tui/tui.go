package tui

import (
	"context"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/mcp"
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
// startNew). The headless entry point (cmd/orqestra/headless.go, WP16) owns
// its own bridge lifecycle the same way (via mcp.StartBridgeAsync below), at
// its own place, since it never calls tui.Run.
func Run(engine *orchestrator.Engine, configName string) error {
	model, err := NewModel(engine, configName)
	if err != nil {
		return fmt.Errorf("init TUI: %w", err)
	}

	bridgeCtx, bridgeCancel := context.WithCancel(context.Background())
	defer bridgeCancel()
	mcp.StartBridgeAsync(bridgeCtx, engine.QuestionBridge)

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
