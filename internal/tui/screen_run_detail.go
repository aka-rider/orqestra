package tui

import (
	"fmt"
	"os"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/xiii/orqestra/internal/orchestrator"
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
	detail        orchestrator.RunDetail
	completeness  orchestrator.RunCompleteness
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

// SetDetail assigns the run detail, analyzes completeness, and resets the step cursor.
func (s *RunDetailScreen) SetDetail(detail orchestrator.RunDetail) {
	s.detail = detail
	s.completeness = orchestrator.AnalyzeRunCompleteness(detail.Path)
	s.stepCursor = 0
	s.focus = RunDetailFocusMenu
}

// SyncViewports updates all run detail viewports from current screen state.
func (s *RunDetailScreen) SyncViewports() {
	// Right pane: step-specific input content.
	var inputBuilder strings.Builder
	if len(s.detail.Steps) > 0 && s.stepCursor < len(s.detail.Steps) {
		step := s.detail.Steps[s.stepCursor]
		if step.ClaudePlanFilePath != "" {
			if data, err := os.ReadFile(step.ClaudePlanFilePath); err == nil && len(data) > 0 {
				inputBuilder.WriteString(renderMarkdown(string(data), s.detailVP.Width()))
			} else {
				inputBuilder.WriteString(renderPrefixedText(lipgloss.NewStyle(), "", s.detail.Prompt, max(1, s.detailVP.Width())))
			}
		} else if s.detail.Prompt != "" {
			inputBuilder.WriteString(renderPrefixedText(lipgloss.NewStyle(), "", s.detail.Prompt, max(1, s.detailVP.Width())))
		} else {
			inputBuilder.WriteString(dimStyle.Render("(no input available)"))
		}
	} else if s.detail.Prompt != "" {
		inputBuilder.WriteString(renderPrefixedText(lipgloss.NewStyle(), "", s.detail.Prompt, max(1, s.detailVP.Width())))
	} else {
		inputBuilder.WriteString(dimStyle.Render("(no input available)"))
	}
	s.detailVP.SetContent(inputBuilder.String())
	s.detailVP.GotoTop()

	// Left pane: agent card menu — preserve scroll position, then apply follow.
	prevOff := s.stepsVP.YOffset()
	s.stepsVP.SetContent(s.viewRunSteps(s.stepsVP.Width()))
	s.stepsVP.SetYOffset(prevOff) // restore position reset by SetContent

	if len(s.detail.Steps) > 0 && s.stepCursor < len(s.detail.Steps) {
		topOfCard := s.stepsScrollOffset()
		cardH := s.cardLineHeight(s.stepCursor)
		bottomOfCard := topOfCard + cardH - 1
		currentOff := s.stepsVP.YOffset()
		vpBottom := currentOff + s.stepsVP.Height() - 1
		if topOfCard < currentOff {
			s.stepsVP.SetYOffset(topOfCard)
		} else if bottomOfCard > vpBottom {
			s.stepsVP.SetYOffset(bottomOfCard - s.stepsVP.Height() + 1)
		}
	}

	// Bottom pane: log
	if len(s.logLines) == 0 {
		s.logVP.SetContent(dimStyle.Render("  (no agent log available)"))
	} else {
		s.logVP.SetContent(strings.Join(s.logLines, "\n"))
	}
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
	incompleteSuffix := ""
	if !s.completeness.Complete {
		incompleteSuffix = warnStyle.Render(" ⚠ INCOMPLETE")
	}
	header := headerStyle.Render(fmt.Sprintf(" %s  %s  %s  %s%s", icon, ts, dur, s.detail.Slug, incompleteSuffix)) + "\n" +
		dividerStyle.Render(strings.Repeat("─", width))

	menuWidth := max(constRunDetailMinMenuW, width*constRunDetailMenuPct/100)
	contentWidth := max(0, width-menuWidth-1)

	// Left: stepsVP spans full body height; right-side border acts as divider.
	menuBorderColor := lipgloss.Color("238")
	if s.focus == RunDetailFocusMenu {
		menuBorderColor = lipgloss.Color("14")
	}
	l := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, true, false, false).
		BorderForeground(menuBorderColor).
		Width(menuWidth - 1).
		Height(s.stepsVP.Height()).
		Render(s.stepsVP.View())

	// Right: Input label + detailVP + Output separator + logVP.
	inputLabel := dimStyle.Render(" Input")
	if s.focus == RunDetailFocusContent {
		inputLabel = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14")).Render("▶ Input")
	}
	sepFill := strings.Repeat("─", max(0, contentWidth-11))
	outputSep := dimStyle.Render("─── Output " + sepFill)
	if s.focus == RunDetailFocusLog {
		outputSep = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Render("─── Output " + sepFill)
	}
	r := inputLabel + "\n" +
		lipgloss.Place(contentWidth, s.detailVP.Height(), lipgloss.Left, lipgloss.Top, s.detailVP.View()) + "\n" +
		outputSep + "\n" +
		s.logVP.View()

	body := lipgloss.JoinHorizontal(lipgloss.Top, l, r)

	// Footer (2 lines)
	footer := dividerStyle.Render(strings.Repeat("─", width)) + "\n" +
		s.viewFooter()

	return header + "\n" + body + "\n" + footer
}

// viewFooter renders the key hint line, adapting to current focus.
func (s RunDetailScreen) viewFooter() string {
	var focusHint string
	switch s.focus {
	case RunDetailFocusMenu:
		focusHint = "menu"
	case RunDetailFocusContent:
		focusHint = "input"
	case RunDetailFocusLog:
		focusHint = "log"
	}
	var restartHint string
	if !s.completeness.Complete {
		restartHint = " | [Ctrl+Shift+R] restart "
	}
	return keyStyle.Render(fmt.Sprintf(
		" [↑↓] select/scroll | [Tab] focus | [Enter] view | [^E] open log | [^Y] history | [Esc] back%s[%s]",
		restartHint, focusHint))
}
