package tui

import (
	"fmt"
	"os"
	"time"

	"github.com/xiii/orqestra/internal/agent"
)

// statusIcon returns a display icon for a step/run status.
func statusIcon(status string) string {
	switch status {
	case "done":
		return "✓"
	case "failed":
		return "✗"
	default:
		return "○"
	}
}

// formatDuration formats a duration for display.
func formatDuration(d time.Duration) string {
	if d == 0 {
		return ""
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
}

// navigateToRunsList loads the runs list and transitions to StateRunsList.
func (m *Model) navigateToRunsList() {
	cwd, err := os.Getwd()
	if err != nil {
		m.lastErr = fmt.Errorf("get cwd: %w", err)
		return
	}
	runs, err := agent.ListRuns(cwd)
	if err != nil {
		m.lastErr = fmt.Errorf("list runs: %w", err)
		return
	}
	m.runsListScreen.SetRuns(runs)
	m.state = StateRunsList
	m.recalculateLayout()
	m.runsListScreen.SyncViewport(m.runsListScreen.viewport.Width())
}
