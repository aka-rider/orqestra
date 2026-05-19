package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/harness"
)

// RunDetailFocus identifies which pane has keyboard focus in the run detail screen.
type RunDetailFocus int

const (
	RunDetailFocusMenu    RunDetailFocus = iota // left pane: agent menu
	RunDetailFocusContent                       // right pane: plan/artifacts
	RunDetailFocusLog                           // bottom pane: agent log
)

// RunDetailScreen manages the run detail inspection view.
type RunDetailScreen struct {
	detail        agent.RunDetail
	stepCursor    int
	focus         RunDetailFocus
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
		focus:    RunDetailFocusMenu,
		detailVP: dvp,
		stepsVP:  svp,
		logVP:    lvp,
	}
}

// SetDetail assigns the run detail and resets the step cursor.
func (s *RunDetailScreen) SetDetail(detail agent.RunDetail) {
	s.detail = detail
	s.stepCursor = 0
	s.focus = RunDetailFocusMenu
}

// SyncViewports updates all run detail viewports from current screen state.
func (s *RunDetailScreen) SyncViewports() {
	// Right pane: plan/content
	var contentBuilder strings.Builder
	if s.detail.Prompt != "" {
		contentBuilder.WriteString(dimStyle.Render("Input Prompt:") + "\n")
		contentBuilder.WriteString(renderPrefixedText(lipgloss.NewStyle(), "", s.detail.Prompt, max(1, s.detailVP.Width())))
		contentBuilder.WriteString("\n    ⇩  ⇩  ⇩\n\n")
	}
	if s.detail.PlanMarkdown != "" {
		rendered := renderMarkdown(s.detail.PlanMarkdown, s.detailVP.Width())
		contentBuilder.WriteString(rendered)
	} else {
		contentBuilder.WriteString(dimStyle.Render("(no plan available)"))
	}
	s.detailVP.SetContent(contentBuilder.String())

	// Left pane: agent card menu
	s.stepsVP.SetContent(s.viewRunSteps(s.stepsVP.Width()))

	// Bottom pane: log
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

	// Prefer local copy in session directory.
	if step.ClaudeSessionLogPath != "" {
		if _, statErr := os.Stat(step.ClaudeSessionLogPath); statErr == nil {
			updates, err := parseSessionLogFile(step.ClaudeSessionLogPath, 200)
			if err != nil || len(updates) == 0 {
				s.logLines = []string{dimStyle.Render("  (empty log)")}
				s.logVP.SetContent(strings.Join(s.logLines, "\n"))
				return
			}
			s.logLines = formatLogUpdates(updates)
			s.logVP.SetContent(strings.Join(s.logLines, "\n"))
			s.logVP.GotoBottom()
			return
		}
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

	updates, err := parseSessionLogFile(logPath, 200)
	if err != nil || len(updates) == 0 {
		s.logLines = []string{dimStyle.Render("  (empty log)")}
		s.logVP.SetContent(strings.Join(s.logLines, "\n"))
		return
	}

	s.logLines = formatLogUpdates(updates)
	s.logVP.SetContent(strings.Join(s.logLines, "\n"))
	s.logVP.GotoBottom()
}

func parseSessionLogFile(path string, maxLines int) ([]harness.StreamUpdate, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	updates, err := harness.ParseSessionLogStream(f)
	if err != nil {
		return nil, err
	}
	if maxLines > 0 && len(updates) > maxLines {
		updates = updates[len(updates)-maxLines:]
	}
	return updates, nil
}

// formatLogUpdates converts parsed stream updates to styled display lines.
func formatLogUpdates(updates []harness.StreamUpdate) []string {
	lines := make([]string, 0, len(updates))
	for _, update := range updates {
		if update.Tool != "" {
			line := "  " + activityToolStyle.Render(update.Tool) + " " + activityPathStyle.Render(update.Detail)
			lines = append(lines, line)
			continue
		}
		if update.Text != "" {
			text := strings.TrimSpace(update.Text)
			if text == "" {
				continue
			}
			line := "  ╶ " + dimStyle.Render(text)
			lines = append(lines, line)
		}
	}
	return lines
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

	// Prefer local copy.
	if step.ClaudeSessionLogPath != "" {
		if _, statErr := os.Stat(step.ClaudeSessionLogPath); statErr == nil {
			cmd := exec.Command("open", step.ClaudeSessionLogPath)
			if err := cmd.Start(); err != nil {
				_ = err // fire-and-forget: opening external editor for log file
			}
			return s, nil
		}
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
		_ = err // fire-and-forget: opening external editor for log file
	}
	return s, nil
}

// viewRunSteps renders the step list as bordered agent cards.
func (s RunDetailScreen) viewRunSteps(width int) string {
	if len(s.detail.Steps) == 0 {
		return dimStyle.Render("  (no steps)")
	}

	// Card inner width = available minus border chars (2 for left+right border)
	cardInnerW := max(10, width-2)
	var b strings.Builder

	for i, step := range s.detail.Steps {
		selected := i == s.stepCursor && s.focus == RunDetailFocusMenu

		icon := statusIcon(step.Status)
		elapsed := step.EndTime.Sub(step.StartTime)
		dur := formatDuration(elapsed)

		// Card header line
		header := fmt.Sprintf("%s %s  %s", icon, step.AgentID, dur)

		// Card body lines
		var body strings.Builder

		// Model line (skip if no metadata available — old runs)
		if step.ModelDisplay != "" {
			modelLine := fmt.Sprintf("Model: %s", step.ModelDisplay)
			if step.Provider != "" {
				modelLine += fmt.Sprintf(" (%s)", step.Provider)
			}
			body.WriteString(modelLine + "\n")
		}

		// Token lines (skip if zero)
		if step.InputTokens > 0 || step.OutputTokens > 0 {
			body.WriteString(fmt.Sprintf("↑ Consumed: %s    ↓ Produced: %s\n",
				formatNumberWithCommas(step.InputTokens),
				formatNumberWithCommas(step.OutputTokens)))
		}

		// Throughput (skip if elapsed is zero or no output)
		if elapsed.Seconds() > 0 && step.OutputTokens > 0 {
			tokPerSec := float64(step.OutputTokens) / elapsed.Seconds()
			body.WriteString(fmt.Sprintf("Throughput: %.1f tok/s\n", tokPerSec))
		}

		// Context bar (skip if no ContextWindow)
		if step.ContextWindow > 0 {
			used := step.InputTokens + step.OutputTokens
			pct := int(used * 100 / step.ContextWindow)
			if pct > 100 {
				pct = 100
			}
			bar := renderProgressBar(pct, 10)
			body.WriteString(fmt.Sprintf("Context: %s %d%% of %s\n",
				bar, pct, formatNumberWithCommas(step.ContextWindow)))
		}

		// Build card content: header + body
		content := header + "\n" + body.String()
		content = strings.TrimRight(content, "\n")

		// Apply border and styling
		borderColor := lipgloss.Color("238")
		if selected {
			borderColor = lipgloss.Color("14")
		}
		cardStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(borderColor).
			Width(cardInnerW)
		if selected {
			cardStyle = cardStyle.Bold(true)
		}

		b.WriteString(cardStyle.Render(content))
		b.WriteString("\n")
	}
	return b.String()
}

// renderProgressBar renders a fixed-width progress bar using block characters.
func renderProgressBar(pct, barWidth int) string {
	filled := barWidth * pct / 100
	empty := barWidth - filled
	return strings.Repeat("█", filled) + strings.Repeat("░", empty)
}

// formatNumberWithCommas formats an int64 with thousands separators.
func formatNumberWithCommas(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var result strings.Builder
	for i, ch := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result.WriteByte(',')
		}
		result.WriteRune(ch)
	}
	return result.String()
}

// Update handles key events for the run detail screen.
func (s RunDetailScreen) Update(msg tea.Msg) (RunDetailScreen, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return s, nil
	}

	// Global keys — not focus-dependent.
	switch keyMsg.String() {
	case "ctrl+e":
		return s.openStepLog()
	case "ctrl+y":
		if s.detail.Path != "" {
			s.PendingIntent = OpenPlanHistoryIntent{
				HistoryDir: filepath.Join(s.detail.Path, "plan-history"),
				ReadOnly:   true,
			}
		}
		return s, nil
	}

	// Focus-dependent key dispatch.
	switch s.focus {
	case RunDetailFocusMenu:
		return s.updateMenu(keyMsg)
	case RunDetailFocusContent:
		return s.updateContent(keyMsg)
	case RunDetailFocusLog:
		return s.updateLog(keyMsg)
	}
	return s, nil
}

func (s RunDetailScreen) updateMenu(msg tea.KeyPressMsg) (RunDetailScreen, tea.Cmd) {
	switch msg.Code {
	case tea.KeyEscape:
		s.PendingIntent = NavigateBackIntent{}
		return s, nil
	case tea.KeyUp:
		if s.stepCursor > 0 {
			s.stepCursor--
			s.LoadStepLog()
			s.SyncViewports()
		}
		return s, nil
	case tea.KeyDown:
		if s.stepCursor < len(s.detail.Steps)-1 {
			s.stepCursor++
			s.LoadStepLog()
			s.SyncViewports()
		}
		return s, nil
	case tea.KeyPgUp:
		s.stepCursor = max(0, s.stepCursor-5)
		s.LoadStepLog()
		s.SyncViewports()
		return s, nil
	case tea.KeyPgDown:
		s.stepCursor = min(max(0, len(s.detail.Steps)-1), s.stepCursor+5)
		s.LoadStepLog()
		s.SyncViewports()
		return s, nil
	case tea.KeyEnter:
		s.focus = RunDetailFocusContent
		return s, nil
	case tea.KeyTab:
		if msg.Mod.Contains(tea.ModShift) {
			s.focus = RunDetailFocusLog
		} else {
			s.focus = RunDetailFocusContent
		}
		return s, nil
	}
	return s, nil
}

func (s RunDetailScreen) updateContent(msg tea.KeyPressMsg) (RunDetailScreen, tea.Cmd) {
	switch msg.Code {
	case tea.KeyEscape:
		s.focus = RunDetailFocusMenu
		return s, nil
	case tea.KeyUp:
		s.detailVP.ScrollUp(1)
		return s, nil
	case tea.KeyDown:
		s.detailVP.ScrollDown(1)
		return s, nil
	case tea.KeyPgUp:
		s.detailVP.HalfPageUp()
		return s, nil
	case tea.KeyPgDown:
		s.detailVP.HalfPageDown()
		return s, nil
	case tea.KeyTab:
		if msg.Mod.Contains(tea.ModShift) {
			s.focus = RunDetailFocusMenu
		} else {
			s.focus = RunDetailFocusLog
		}
		return s, nil
	}
	return s, nil
}

func (s RunDetailScreen) updateLog(msg tea.KeyPressMsg) (RunDetailScreen, tea.Cmd) {
	switch msg.Code {
	case tea.KeyEscape:
		s.focus = RunDetailFocusMenu
		return s, nil
	case tea.KeyUp:
		s.logVP.ScrollUp(1)
		return s, nil
	case tea.KeyDown:
		s.logVP.ScrollDown(1)
		return s, nil
	case tea.KeyPgUp:
		s.logVP.HalfPageUp()
		return s, nil
	case tea.KeyPgDown:
		s.logVP.HalfPageDown()
		return s, nil
	case tea.KeyTab:
		if msg.Mod.Contains(tea.ModShift) {
			s.focus = RunDetailFocusContent
		} else {
			s.focus = RunDetailFocusMenu
		}
		return s, nil
	}
	return s, nil
}

// View renders the run detail screen.
func (s RunDetailScreen) View(width, height int) string {
	if height < minHeight {
		return " Terminal too small. Please resize."
	}

	// Header (2 lines)
	icon := statusIcon(s.detail.Status)
	dur := formatDuration(s.detail.Duration)
	ts := s.detail.Timestamp.Format("2006-01-02 15:04:05")
	header := headerStyle.Render(fmt.Sprintf(" %s  %s  %s  %s", icon, ts, dur, s.detail.Slug)) + "\n" +
		dividerStyle.Render(strings.Repeat("─", width))

	// Layout: left pane = agent menu, right pane = plan content
	menuWidth := max(constRunDetailMinMenuW, width*constRunDetailMenuPct/100)
	contentWidth := max(0, width-menuWidth-1) // 1 for vertical divider
	upperHeight := s.detailVP.Height()

	// Left: agent menu with right-side border as divider
	menuBorderColor := lipgloss.Color("238")
	if s.focus == RunDetailFocusMenu {
		menuBorderColor = lipgloss.Color("14")
	}
	l := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, true, false, false).
		BorderForeground(menuBorderColor).
		Width(menuWidth - 1). // -1 accounts for border char
		Height(upperHeight).
		Render(s.stepsVP.View())

	r := lipgloss.Place(contentWidth, upperHeight, lipgloss.Left, lipgloss.Top, s.detailVP.View())
	upper := lipgloss.JoinHorizontal(lipgloss.Top, l, r)

	// Divider between upper and lower panes (1 line)
	divider := dividerStyle.Render(strings.Repeat("─", width))

	// Lower zone: log viewport
	lower := s.logVP.View()

	// Footer (2 lines)
	footer := dividerStyle.Render(strings.Repeat("─", width)) + "\n" +
		s.viewFooter()

	return header + "\n" + upper + "\n" + divider + "\n" + lower + "\n" + footer
}

// viewFooter renders the key hint line, adapting to current focus.
func (s RunDetailScreen) viewFooter() string {
	var focusHint string
	switch s.focus {
	case RunDetailFocusMenu:
		focusHint = "menu"
	case RunDetailFocusContent:
		focusHint = "plan"
	case RunDetailFocusLog:
		focusHint = "log"
	}
	return keyStyle.Render(fmt.Sprintf(
		" [↑↓] select/scroll | [Tab] focus | [Enter] view | [^E] open log | [^Y] history | [Esc] back  [%s]",
		focusHint))
}
