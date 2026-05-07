package tui

import (
	"github.com/xiii/orqestra/internal/orchestrator"
)

// --- TUI Messages (tea.Msg types) ---

// OrchestratorEventMsg wraps an orchestrator event for the TUI.
type OrchestratorEventMsg struct{ Event orchestrator.Event }
