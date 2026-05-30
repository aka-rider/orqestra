package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// renderFrame renders a single frame with its border, header, and content.
func renderFrame(f *Frame, width int, animFrame int) string {
	if width < 20 {
		width = 20
	}

	header := renderFrameHeader(f, animFrame)
	body := renderFrameBody(f, width)

	// Use the appropriate border style based on frame state
	style := frameStyle(f)
	innerWidth := width - 4 // border (2) + padding (2)
	if innerWidth < 10 {
		innerWidth = 10
	}

	content := body
	if content == "" {
		content = " " // ensure non-empty frame body
	}

	rendered := style.
		Width(innerWidth).
		Render(content)

	// Replace the top border's first segment with the header
	return injectHeader(rendered, header, width)
}

// renderFrameHeader builds the frame header string: "─ AgentID (model) ── status ─"
func renderFrameHeader(f *Frame, animFrame int) string {
	var parts []string

	switch f.Kind {
	case AgentFrame:
		parts = append(parts, f.AgentID)
		if f.AgentModel != "" {
			parts = append(parts, fmt.Sprintf("(%s)", f.AgentModel))
		}
	case PlanFrame:
		parts = append(parts, "Plan")
	case CompletionFrame:
		parts = append(parts, "Complete")
	case ErrorFrame:
		parts = append(parts, "Error")
	}

	label := strings.Join(parts, " ")

	// State indicator on the right
	var indicator string
	switch f.State {
	case FrameInit:
		indicator = "…"
	case FrameInProgress:
		if len(shimmerFrames) > 0 {
			indicator = shimmerFrames[animFrame%len(shimmerFrames)]
		} else {
			indicator = "···"
		}
	case FrameFinished:
		indicator = fmt.Sprintf("%s %s", Pass, formatElapsed(f.Elapsed))
	}

	return fmt.Sprintf(" %s %s %s ", label, frameSepStr, indicator)
}

// renderFrameBody renders the interleaved content parts of a frame.
func renderFrameBody(f *Frame, width int) string {
	innerWidth := width - 4 // border + padding
	if innerWidth < 10 {
		innerWidth = 10
	}

	var b strings.Builder
	for _, part := range f.Parts {
		if part.IsText {
			// Render text lines, trimming trailing newline for the last entry
			text := strings.TrimRight(part.Text, "\n")
			lines := strings.Split(text, "\n")
			for _, line := range lines {
				if len(line) > innerWidth {
					line = line[:innerWidth]
				}
				b.WriteString(line)
				b.WriteByte('\n')
			}
		} else {
			b.WriteString(renderToolBlock(part.Tool, innerWidth))
			b.WriteByte('\n')
		}
	}

	// Render partial line with cursor indicator
	if f.Partial != "" {
		partial := f.Partial
		if len(partial) > innerWidth-2 {
			partial = partial[:innerWidth-2]
		}
		b.WriteString(framePartialIndicator + partial)
		b.WriteByte('\n')
	}

	// Plan gate hint when awaiting decision
	if f.Kind == PlanFrame && f.State == FrameInProgress {
		b.WriteByte('\n')
		b.WriteString(planHintStyle.Render("[^A] accept  [Enter] comment  [^E] edit  [^D] diff"))
		b.WriteByte('\n')
	}

	return strings.TrimRight(b.String(), "\n")
}

// renderToolBlock renders a tool invocation as an inner bordered sub-block.
func renderToolBlock(tb ToolBlock, maxWidth int) string {
	icon := tb.Icon
	if icon == "" {
		icon = IconForAction(tb.Name)
	}

	label := fmt.Sprintf("%s %s", icon, tb.Name)
	if tb.Detail != "" {
		label += " " + tb.Detail
	}

	innerWidth := maxWidth - 4 // sub-block border + padding
	if innerWidth < 10 {
		innerWidth = 10
	}
	if len(label) > innerWidth {
		label = label[:innerWidth]
	}

	return toolBlockStyle.Width(innerWidth).Render(label)
}

// injectHeader replaces the beginning of the top border line with the header text.
func injectHeader(rendered, header string, width int) string {
	lines := strings.Split(rendered, "\n")
	if len(lines) == 0 {
		return rendered
	}

	topLine := lines[0]
	// The top border line starts with "╭" — inject header after it
	headerStyled := frameHeaderStyle.Render(header)

	// Calculate how much of the border to replace
	headerRunes := []rune(headerStyled)
	topRunes := []rune(topLine)

	if len(topRunes) > 2 && len(headerRunes) < len(topRunes)-2 {
		// Keep the corner, inject header, keep remaining border
		newTop := string(topRunes[:1]) + string(headerRunes)
		remaining := len(topRunes) - 1 - len(headerRunes)
		if remaining > 0 {
			newTop += strings.Repeat("─", remaining-1) + string(topRunes[len(topRunes)-1:])
		}
		lines[0] = newTop
	}

	return strings.Join(lines, "\n")
}

// formatElapsed formats a duration as a compact string.
func formatElapsed(d time.Duration) string {
	if d < time.Second {
		return "<1s"
	}
	s := int(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	m := s / 60
	s = s % 60
	if s == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dm%ds", m, s)
}

const (
	frameSepStr           = "──"
	framePartialIndicator = "▎"
)

// Frame border styles
var (
	// Outer frame border for agent frames
	frameAgentBorder = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("7")).
				Padding(0, 1)

	// Frame border when in-progress (slightly brighter)
	frameActiveBorder = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("12")).
				Padding(0, 1)

	// Frame border for plan frames
	framePlanBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("14")).
			Padding(0, 1)

	// Frame border for error frames
	frameErrorBorder = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("1")).
				Padding(0, 1)

	// Inner tool sub-block border (dimmer, nested)
	toolBlockStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1)

	// Header text style within the border
	frameHeaderStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("15")).
				Bold(true)

	// Plan gate action hints
	planHintStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")).
			Faint(true)
)

// frameStyle returns the appropriate border style for a frame.
func frameStyle(f *Frame) lipgloss.Style {
	switch {
	case f.Kind == ErrorFrame:
		return frameErrorBorder
	case f.Kind == PlanFrame:
		return framePlanBorder
	case f.State == FrameInProgress:
		return frameActiveBorder
	default:
		return frameAgentBorder
	}
}
