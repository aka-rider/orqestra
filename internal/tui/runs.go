package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/harness"
)

// viewRunsListScreen renders the historical runs list.
func (m Model) viewRunsListScreen() string {
	w := m.effectiveWidth()
	h := m.height
	if h < minHeight {
		return " Terminal too small. Please resize."
	}

	// Header
	header := headerStyle.Render(" Orqestra — Runs History") + "\n" +
		dividerStyle.Render(strings.Repeat("─", w))

	// Footer
	footer := dividerStyle.Render(strings.Repeat("─", w)) + "\n" +
		keyStyle.Render(" [↑↓/j/k] navigate | [Enter] view | [Esc] back  [^C^C] quit")

	// Body
	if len(m.runs) == 0 {
		body := dimStyle.Render("\n  No runs found. Run a pipeline first.\n")
		m.runsVP.SetContent(body)
	} else {
		var b strings.Builder
		for i, run := range m.runs {
			icon := statusIcon(run.Status)
			dur := formatDuration(run.Duration)
			ts := run.Timestamp.Format("2006-01-02 15:04:05")

			var tokens string
			if run.TotalTokens > 0 {
				tokens = fmt.Sprintf("  %dk tok", run.TotalTokens/1000)
			}

			line1 := fmt.Sprintf("  %s  %s  %s%s  %s", icon, ts, dur, tokens, run.Slug)
			if i == m.runsCursor {
				line1 = selectedStyle.Render(line1)
			}
			b.WriteString(line1 + "\n")

			prompt := run.Prompt
			if len(prompt) > w-6 {
				prompt = prompt[:w-9] + "..."
			}
			line2 := dimStyle.Render("     " + prompt)
			b.WriteString(line2 + "\n")
		}
		m.runsVP.SetContent(b.String())
	}

	return header + "\n" + m.runsVP.View() + "\n" + footer
}

// viewRunDetailScreen renders the 3-zone run detail view.
func (m Model) viewRunDetailScreen() string {
	w := m.effectiveWidth()
	h := m.height
	if h < minHeight {
		return " Terminal too small. Please resize."
	}

	// Header
	icon := statusIcon(m.runDetail.Status)
	dur := formatDuration(m.runDetail.Duration)
	ts := m.runDetail.Timestamp.Format("2006-01-02 15:04:05")
	header := headerStyle.Render(fmt.Sprintf(" %s  %s  %s  %s", icon, ts, dur, m.runDetail.Slug)) + "\n" +
		dividerStyle.Render(strings.Repeat("─", w))

	// Upper zone: left (prompt → plan) + right (step menu)
	// Left content
	var leftContent strings.Builder
	if m.runDetail.Prompt != "" {
		leftContent.WriteString(dimStyle.Render("Input Prompt:") + "\n")
		leftContent.WriteString(m.runDetail.Prompt + "\n")
		leftContent.WriteString("\n    ⇩  ⇩  ⇩\n\n")
	}
	if m.runDetail.PlanMarkdown != "" {
		rendered := renderMarkdown(m.runDetail.PlanMarkdown, m.runDetailVP.Width)
		leftContent.WriteString(rendered)
	} else {
		leftContent.WriteString(dimStyle.Render("(no plan available)"))
	}
	m.runDetailVP.SetContent(leftContent.String())

	// Right content — step menu
	m.runStepsVP.SetContent(m.viewRunSteps(m.runStepsVP.Width))

	contentWidth := max(0, int(float64(w)*splitRatio))
	sidebarWidth := max(0, w-contentWidth-1)
	upperHeight := m.runDetailVP.Height
	upper := joinSplitView(m.runDetailVP.View(), m.runStepsVP.View(), contentWidth, sidebarWidth, upperHeight)

	// Divider
	divider := dividerStyle.Render(strings.Repeat("─", w))

	// Lower zone — raw JSONL log
	if len(m.runLogLines) == 0 {
		m.runLogVP.SetContent(dimStyle.Render("  (no agent log available)"))
	} else {
		m.runLogVP.SetContent(strings.Join(m.runLogLines, "\n"))
	}
	lower := m.runLogVP.View()

	// Footer
	footer := dividerStyle.Render(strings.Repeat("─", w)) + "\n" +
		keyStyle.Render(" [↑↓] scroll log | [j/k] step | [PgUp/PgDn] scroll plan | [Ctrl+E] open log | [Esc] back  [^C^C] quit")

	return header + "\n" + upper + "\n" + divider + "\n" + lower + "\n" + footer
}

// viewRunSteps renders the step list as a vertical menu for the detail view.
func (m Model) viewRunSteps(width int) string {
	if len(m.runDetail.Steps) == 0 {
		return dimStyle.Render("  (no steps)")
	}
	var b strings.Builder
	for i, step := range m.runDetail.Steps {
		icon := statusIcon(step.Status)
		elapsed := step.EndTime.Sub(step.StartTime)
		dur := formatDuration(elapsed)

		var tokens string
		total := step.InputTokens + step.OutputTokens
		if total > 0 {
			tokens = fmt.Sprintf(" %dk", total/1000)
		}

		line := fmt.Sprintf(" %s %s %s%s", icon, step.AgentID, dur, tokens)
		if i == m.runStepCursor {
			b.WriteString(selectedStyle.Render(line) + "\n")
		} else {
			b.WriteString(dimStyle.Render(line) + "\n")
		}
	}
	return b.String()
}

// handleRunsListKey handles key events in the runs list view.
func (m Model) handleRunsListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.state = StatePrompt
		m.recalculateLayout()
		return m, nil
	case tea.KeyEnter:
		if len(m.runs) == 0 {
			return m, nil
		}
		detail, err := agent.LoadRunDetail(m.runs[m.runsCursor].Path)
		if err != nil {
			m.lastErr = err
			return m, nil
		}
		m.runDetail = detail
		m.runStepCursor = 0
		m.state = StateRunDetail
		m.loadStepLog()
		m.recalculateLayout()
		return m, nil
	case tea.KeyUp:
		if m.runsCursor > 0 {
			m.runsCursor--
		}
		return m, nil
	case tea.KeyDown:
		if m.runsCursor < len(m.runs)-1 {
			m.runsCursor++
		}
		return m, nil
	case tea.KeyPgUp:
		m.runsVP.HalfViewUp()
		return m, nil
	case tea.KeyPgDown:
		m.runsVP.HalfViewDown()
		return m, nil
	}

	switch msg.String() {
	case "j":
		if m.runsCursor < len(m.runs)-1 {
			m.runsCursor++
		}
	case "k":
		if m.runsCursor > 0 {
			m.runsCursor--
		}
	case "q":
		m.state = StatePrompt
		m.recalculateLayout()
	}
	return m, nil
}

// handleRunDetailKey handles key events in the run detail view.
func (m Model) handleRunDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.state = StateRunsList
		m.recalculateLayout()
		return m, nil
	case tea.KeyUp:
		m.runLogVP.LineUp(1)
		return m, nil
	case tea.KeyDown:
		m.runLogVP.LineDown(1)
		return m, nil
	case tea.KeyPgUp:
		m.runDetailVP.HalfViewUp()
		return m, nil
	case tea.KeyPgDown:
		m.runDetailVP.HalfViewDown()
		return m, nil
	case tea.KeyCtrlE:
		return m.openStepLog()
	}

	switch msg.String() {
	case "j":
		if m.runStepCursor < len(m.runDetail.Steps)-1 {
			m.runStepCursor++
			m.loadStepLog()
		}
	case "k":
		if m.runStepCursor > 0 {
			m.runStepCursor--
			m.loadStepLog()
		}
	}
	return m, nil
}

// loadStepLog populates m.runLogLines from the selected step's Claude JSONL.
func (m *Model) loadStepLog() {
	if len(m.runDetail.Steps) == 0 || m.runStepCursor >= len(m.runDetail.Steps) {
		m.runLogLines = []string{dimStyle.Render("  (no agent log available)")}
		m.runLogVP.SetContent(strings.Join(m.runLogLines, "\n"))
		return
	}

	step := m.runDetail.Steps[m.runStepCursor]
	if step.ClaudeSessionID == "" {
		m.runLogLines = []string{dimStyle.Render("  (no agent log available)")}
		m.runLogVP.SetContent(strings.Join(m.runLogLines, "\n"))
		return
	}

	cwd, err := os.Getwd()
	if err != nil {
		m.runLogLines = []string{dimStyle.Render("  (cannot determine cwd)")}
		m.runLogVP.SetContent(strings.Join(m.runLogLines, "\n"))
		return
	}

	logPath, err := harness.ResolveSessionLogPath(cwd, step.ClaudeSessionID)
	if err != nil {
		m.runLogLines = []string{dimStyle.Render(fmt.Sprintf("  (log not found: %s)", step.ClaudeSessionID))}
		m.runLogVP.SetContent(strings.Join(m.runLogLines, "\n"))
		return
	}

	entries, err := harness.ParseSessionLog(logPath, 200)
	if err != nil || len(entries) == 0 {
		m.runLogLines = []string{dimStyle.Render("  (empty log)")}
		m.runLogVP.SetContent(strings.Join(m.runLogLines, "\n"))
		return
	}

	m.runLogLines = make([]string, 0, len(entries))
	for _, entry := range entries {
		switch entry.Kind {
		case harness.LogEntryToolUse:
			line := "  " + activityToolStyle.Render(entry.ToolName) + " " + activityPathStyle.Render(entry.Detail)
			m.runLogLines = append(m.runLogLines, line)
		case harness.LogEntryText:
			line := "  ╶ " + dimStyle.Render(entry.Detail)
			m.runLogLines = append(m.runLogLines, line)
		}
	}
	m.runLogVP.SetContent(strings.Join(m.runLogLines, "\n"))
	m.runLogVP.GotoBottom()
}

// openStepLog opens the JSONL file for the selected step in the system editor.
func (m Model) openStepLog() (tea.Model, tea.Cmd) {
	if len(m.runDetail.Steps) == 0 || m.runStepCursor >= len(m.runDetail.Steps) {
		return m, nil
	}
	step := m.runDetail.Steps[m.runStepCursor]
	if step.ClaudeSessionID == "" {
		m.lastErr = fmt.Errorf("no session ID for step %q", step.AgentID)
		return m, nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		m.lastErr = fmt.Errorf("get cwd: %w", err)
		return m, nil
	}

	logPath, err := harness.ResolveSessionLogPath(cwd, step.ClaudeSessionID)
	if err != nil {
		m.lastErr = fmt.Errorf("resolve log path: %w", err)
		return m, nil
	}

	cmd := exec.Command("open", logPath)
	if err := cmd.Start(); err != nil {
		m.lastErr = fmt.Errorf("open log: %w", err)
	}
	return m, nil
}

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
	m.runs = runs
	m.runsCursor = 0
	m.state = StateRunsList
	m.recalculateLayout()
}
