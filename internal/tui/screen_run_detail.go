package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/harness"
)

// RunDetailScreen manages the run detail inspection view.
type RunDetailScreen struct {
	detail        agent.RunDetail
	stepCursor    int
	logLines      []string
	detailVP      viewport.Model
	stepsVP       viewport.Model
	logVP         viewport.Model
	PendingIntent tea.Msg // set by Update, consumed by parent
}

// NewRunDetailScreen creates a new run detail screen.
func NewRunDetailScreen() RunDetailScreen {
	dvp := viewport.New()
	dvp.MouseWheelEnabled = true
	svp := viewport.New()
	svp.MouseWheelEnabled = true
	lvp := viewport.New()
	lvp.MouseWheelEnabled = true
	return RunDetailScreen{
		detailVP: dvp,
		stepsVP:  svp,
		logVP:    lvp,
	}
}

// SetDetail assigns the run detail and resets the step cursor.
func (s *RunDetailScreen) SetDetail(detail agent.RunDetail) {
	s.detail = detail
	s.stepCursor = 0
}

// SyncViewports updates all run detail viewports from current screen state.
func (s *RunDetailScreen) SyncViewports() {
	// Left content
	var leftContent strings.Builder
	if s.detail.Prompt != "" {
		leftContent.WriteString(dimStyle.Render("Input Prompt:") + "\n")
		leftContent.WriteString(s.detail.Prompt + "\n")
		leftContent.WriteString("\n    ⇩  ⇩  ⇩\n\n")
	}
	if s.detail.PlanMarkdown != "" {
		rendered := renderMarkdown(s.detail.PlanMarkdown, s.detailVP.Width())
		leftContent.WriteString(rendered)
	} else {
		leftContent.WriteString(dimStyle.Render("(no plan available)"))
	}
	s.detailVP.SetContent(leftContent.String())

	// Right content — step menu
	s.stepsVP.SetContent(s.viewRunSteps(s.stepsVP.Width()))

	// Log content
	if len(s.logLines) == 0 {
		s.logVP.SetContent(dimStyle.Render("  (no agent log available)"))
	} else {
		s.logVP.SetContent(strings.Join(s.logLines, "\n"))
	}
}

// LoadStepLog populates logLines from the selected step's Claude JSONL.
func (s *RunDetailScreen) LoadStepLog() {
	if len(s.detail.Steps) == 0 || s.stepCursor >= len(s.detail.Steps) {
		s.logLines = []string{dimStyle.Render("  (no agent log available)")}
		s.logVP.SetContent(strings.Join(s.logLines, "\n"))
		return
	}

	step := s.detail.Steps[s.stepCursor]
	if step.ClaudeSessionID == "" {
		s.logLines = []string{dimStyle.Render("  (no agent log available)")}
		s.logVP.SetContent(strings.Join(s.logLines, "\n"))
		return
	}

	cwd, err := os.Getwd()
	if err != nil {
		s.logLines = []string{dimStyle.Render("  (cannot determine cwd)")}
		s.logVP.SetContent(strings.Join(s.logLines, "\n"))
		return
	}

	logPath, err := harness.ResolveSessionLogPath(cwd, step.ClaudeSessionID)
	if err != nil {
		s.logLines = []string{dimStyle.Render(fmt.Sprintf("  (log not found: %s)", step.ClaudeSessionID))}
		s.logVP.SetContent(strings.Join(s.logLines, "\n"))
		return
	}

	entries, err := harness.ParseSessionLog(logPath, 200)
	if err != nil || len(entries) == 0 {
		s.logLines = []string{dimStyle.Render("  (empty log)")}
		s.logVP.SetContent(strings.Join(s.logLines, "\n"))
		return
	}

	s.logLines = make([]string, 0, len(entries))
	for _, entry := range entries {
		switch entry.Kind {
		case harness.LogEntryToolUse:
			line := "  " + activityToolStyle.Render(entry.ToolName) + " " + activityPathStyle.Render(entry.Detail)
			s.logLines = append(s.logLines, line)
		case harness.LogEntryText:
			line := "  ╶ " + dimStyle.Render(entry.Detail)
			s.logLines = append(s.logLines, line)
		}
	}
	s.logVP.SetContent(strings.Join(s.logLines, "\n"))
	s.logVP.GotoBottom()
}

// openStepLog opens the JSONL file for the selected step in the system editor.
func (s RunDetailScreen) openStepLog() (RunDetailScreen, tea.Cmd) {
	if len(s.detail.Steps) == 0 || s.stepCursor >= len(s.detail.Steps) {
		return s, nil
	}
	step := s.detail.Steps[s.stepCursor]
	if step.ClaudeSessionID == "" {
		return s, nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return s, nil
	}

	logPath, err := harness.ResolveSessionLogPath(cwd, step.ClaudeSessionID)
	if err != nil {
		return s, nil
	}

	cmd := exec.Command("open", logPath)
	if err := cmd.Start(); err != nil {
		// Swallow — opening log is best-effort
		_ = err // fire-and-forget: opening external editor for log file
	}
	return s, nil
}

// viewRunSteps renders the step list as a vertical menu.
func (s RunDetailScreen) viewRunSteps(width int) string {
	if len(s.detail.Steps) == 0 {
		return dimStyle.Render("  (no steps)")
	}
	var b strings.Builder
	for i, step := range s.detail.Steps {
		icon := statusIcon(step.Status)
		elapsed := step.EndTime.Sub(step.StartTime)
		dur := formatDuration(elapsed)

		var tokens string
		total := step.InputTokens + step.OutputTokens
		if total > 0 {
			tokens = fmt.Sprintf(" %dk", total/1000)
		}

		line := fmt.Sprintf(" %s %s %s%s", icon, step.AgentID, dur, tokens)
		if i == s.stepCursor {
			b.WriteString(selectedStyle.Render(line) + "\n")
		} else {
			b.WriteString(dimStyle.Render(line) + "\n")
		}
	}
	return b.String()
}

// Update handles key events for the run detail screen.
func (s RunDetailScreen) Update(msg tea.Msg) (RunDetailScreen, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return s, nil
	}

	if keyMsg.String() == "ctrl+e" {
		return s.openStepLog()
	}
	switch keyMsg.Code {
	case tea.KeyEscape:
		s.PendingIntent = NavigateBackIntent{}
		return s, nil
	case tea.KeyUp:
		s.logVP.ScrollUp(1)
		return s, nil
	case tea.KeyDown:
		s.logVP.ScrollDown(1)
		return s, nil
	case tea.KeyPgUp:
		s.detailVP.HalfPageUp()
		return s, nil
	case tea.KeyPgDown:
		s.detailVP.HalfPageDown()
		return s, nil
	}

	switch keyMsg.String() {
	case "j":
		if s.stepCursor < len(s.detail.Steps)-1 {
			s.stepCursor++
			s.LoadStepLog()
			s.SyncViewports()
		}
	case "k":
		if s.stepCursor > 0 {
			s.stepCursor--
			s.LoadStepLog()
			s.SyncViewports()
		}
	}
	return s, nil
}

// View renders the run detail screen.
func (s RunDetailScreen) View(width, height int) string {
	if height < minHeight {
		return " Terminal too small. Please resize."
	}

	// Header
	icon := statusIcon(s.detail.Status)
	dur := formatDuration(s.detail.Duration)
	ts := s.detail.Timestamp.Format("2006-01-02 15:04:05")
	header := headerStyle.Render(fmt.Sprintf(" %s  %s  %s  %s", icon, ts, dur, s.detail.Slug)) + "\n" +
		dividerStyle.Render(strings.Repeat("─", width))

	contentWidth := max(0, int(float64(width)*splitRatio))
	sidebarWidth := max(0, width-contentWidth-1)
	upperHeight := s.detailVP.Height()
	upper := joinSplitView(s.detailVP.View(), s.stepsVP.View(), contentWidth, sidebarWidth, upperHeight)

	// Divider
	divider := dividerStyle.Render(strings.Repeat("─", width))

	// Lower zone — read from viewport (already synced in Update)
	lower := s.logVP.View()

	// Footer
	footer := dividerStyle.Render(strings.Repeat("─", width)) + "\n" +
		keyStyle.Render(" [↑↓] scroll log | [j/k] step | [PgUp/PgDn] scroll plan | [Ctrl+E] open log | [Esc] back  [^C^C] quit")

	return header + "\n" + upper + "\n" + divider + "\n" + lower + "\n" + footer
}
