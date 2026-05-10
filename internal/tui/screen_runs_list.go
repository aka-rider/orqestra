package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/xiii/orqestra/internal/agent"
)

// RunsListScreen manages the historical runs list view.
type RunsListScreen struct {
	runs     []agent.RunSummary
	cursor   int
	viewport viewport.Model
}

// NewRunsListScreen creates a new runs list screen.
func NewRunsListScreen() RunsListScreen {
	vp := viewport.New(0, 0)
	vp.MouseWheelEnabled = true
	return RunsListScreen{viewport: vp}
}

// SetRuns assigns the runs list and resets the cursor.
func (s *RunsListScreen) SetRuns(runs []agent.RunSummary) {
	s.runs = runs
	s.cursor = 0
}

// SelectedRun returns the currently selected run, if any.
func (s RunsListScreen) SelectedRun() (agent.RunSummary, bool) {
	if s.cursor >= 0 && s.cursor < len(s.runs) {
		return s.runs[s.cursor], true
	}
	return agent.RunSummary{}, false
}

// SyncViewport updates the viewport content from current screen state.
func (s *RunsListScreen) SyncViewport(width int) {
	if len(s.runs) == 0 {
		s.viewport.SetContent(dimStyle.Render("\n  No runs found. Run a pipeline first.\n"))
		return
	}
	var b strings.Builder
	for i, run := range s.runs {
		icon := statusIcon(run.Status)
		dur := formatDuration(run.Duration)
		ts := run.Timestamp.Format("2006-01-02 15:04:05")

		var tokens string
		if run.TotalTokens > 0 {
			tokens = fmt.Sprintf("  %dk tok", run.TotalTokens/1000)
		}

		line1 := fmt.Sprintf("  %s  %s  %s%s  %s", icon, ts, dur, tokens, run.Slug)
		if i == s.cursor {
			line1 = selectedStyle.Render(line1)
		}
		b.WriteString(line1 + "\n")

		prompt := run.Prompt
		if len(prompt) > width-6 {
			prompt = prompt[:width-9] + "..."
		}
		line2 := dimStyle.Render("     " + prompt)
		b.WriteString(line2 + "\n")
	}
	s.viewport.SetContent(b.String())
}

// Update handles key events for the runs list screen.
func (s RunsListScreen) Update(msg tea.Msg) (RunsListScreen, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return s, nil
	}

	switch keyMsg.Type {
	case tea.KeyEsc:
		return s, intentCmd(NavigateBackIntent{})
	case tea.KeyEnter:
		if len(s.runs) == 0 {
			return s, nil
		}
		return s, intentCmd(NavigateToRunDetailIntent{RunIndex: s.cursor})
	case tea.KeyUp:
		if s.cursor > 0 {
			s.cursor--
			s.SyncViewport(s.viewport.Width)
		}
		return s, nil
	case tea.KeyDown:
		if s.cursor < len(s.runs)-1 {
			s.cursor++
			s.SyncViewport(s.viewport.Width)
		}
		return s, nil
	case tea.KeyPgUp:
		s.viewport.HalfViewUp()
		return s, nil
	case tea.KeyPgDown:
		s.viewport.HalfViewDown()
		return s, nil
	}

	switch keyMsg.String() {
	case "j":
		if s.cursor < len(s.runs)-1 {
			s.cursor++
			s.SyncViewport(s.viewport.Width)
		}
	case "k":
		if s.cursor > 0 {
			s.cursor--
			s.SyncViewport(s.viewport.Width)
		}
	case "q":
		return s, intentCmd(NavigateBackIntent{})
	}
	return s, nil
}

// View renders the runs list screen.
func (s RunsListScreen) View(width, height int) string {
	if height < minHeight {
		return " Terminal too small. Please resize."
	}

	// Header
	header := headerStyle.Render(" Orqestra — Runs History") + "\n" +
		dividerStyle.Render(strings.Repeat("─", width))

	// Footer
	footer := dividerStyle.Render(strings.Repeat("─", width)) + "\n" +
		keyStyle.Render(" [↑↓/j/k] navigate | [Enter] view | [Esc] back  [^C^C] quit")

	return header + "\n" + s.viewport.View() + "\n" + footer
}
