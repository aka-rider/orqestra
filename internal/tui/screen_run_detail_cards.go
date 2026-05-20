package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

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

// cardLineHeight returns the number of rendered lines the card at idx occupies
// in stepsVP, mirroring the conditional body lines in viewRunSteps exactly.
func (s RunDetailScreen) cardLineHeight(idx int) int {
	step := s.detail.Steps[idx]
	elapsed := step.EndTime.Sub(step.StartTime)
	inner := 1 // header line always
	if step.ModelDisplay != "" {
		inner++
	}
	if step.InputTokens > 0 || step.OutputTokens > 0 {
		inner++
	}
	if elapsed.Seconds() > 0 && step.OutputTokens > 0 {
		inner++
	}
	if step.ContextWindow > 0 {
		inner++
	}
	return inner + 2 + 1 // +2 rounded border top/bottom, +1 trailing "\n" after card
}

// stepsScrollOffset returns the total line offset to the top of the card at
// stepCursor in the stepsVP content.
func (s RunDetailScreen) stepsScrollOffset() int {
	offset := 0
	for i := 0; i < s.stepCursor; i++ {
		offset += s.cardLineHeight(i)
	}
	return offset
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
