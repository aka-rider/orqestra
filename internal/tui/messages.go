package tui

import (
	"time"

	"github.com/xiii/orqestra/internal/orchestrator"
)

// --- TUI Messages (tea.Msg types) ---

// OrchestratorEventMsg wraps an orchestrator event for the TUI.
type OrchestratorEventMsg struct{ Event orchestrator.Event }

// tickMsg fires every second to refresh elapsed timers and live output.
type tickMsg time.Time
