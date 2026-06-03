package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// renderFrame renders a single frame with its border, header, and content.
func renderFrame(f *Frame, width int, animFrame int, focused bool) string {
	if width < 20 {
		width = 20
	}

	// Collapsed finished frames: compact 3-line block with summary
	if f.Collapsed && f.State == FrameFinished {
		header := renderFrameHeader(f, animFrame)
		innerWidth := width - 4
		if innerWidth < 10 {
			innerWidth = 10
		}
		rendered := frameStyle(f, focused).Width(innerWidth).Render(collapsedSummary(f, innerWidth))
		return injectHeader(rendered, header, width)
	}

	header := renderFrameHeader(f, animFrame)
	body := renderFrameBody(f, width)

	// Use the appropriate border style based on frame state
	style := frameStyle(f, focused)
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

// collapsedSummary returns a single-line summary for a collapsed frame.
func collapsedSummary(f *Frame, innerWidth int) string {
	for _, part := range f.Parts {
		if !part.IsText {
			continue
		}
		for _, line := range strings.SplitN(part.Text, "\n", 5) {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if len(line) > innerWidth-1 {
				return line[:innerWidth-1] + "…"
			}
			return line
		}
	}
	switch f.Kind {
	case PlanFrame:
		return "Plan"
	case CompletionFrame:
		return "Complete"
	case ErrorFrame:
		return "Error"
	default:
		return f.AgentID
	}
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
		if len(spinningFrames) > 0 {
			indicator = spinningFrames[animFrame%len(spinningFrames)]
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

	// StreamingCollapsed: in-progress frames with ^O toggle show only header text.
	if f.State == FrameInProgress && f.StreamingCollapsed {
		for _, part := range f.Parts {
			if !part.IsText {
				continue
			}
			text := strings.TrimRight(part.Text, "\n")
			lines := strings.Split(text, "\n")
			for _, line := range lines {
				if len(line) > innerWidth {
					line = line[:innerWidth]
				}
				return line
			}
		}
		return ""
	}

	// Count total tool parts to decide whether overflow applies.
	toolCount := 0
	for _, p := range f.Parts {
		if !p.IsText {
			toolCount++
		}
	}
	hiddenTools := 0
	if !f.ToolsExpanded && toolCount > toolPreviewLimit {
		hiddenTools = toolCount - toolPreviewLimit
	}

	var b strings.Builder
	toolsSeen := 0
	indicatorEmitted := false
	for _, part := range f.Parts {
		if part.IsText {
			// Use cached glamour-rendered markdown when available.
			if part.MarkdownRendered != "" {
				b.WriteString(part.MarkdownRendered)
				b.WriteByte('\n')
				continue
			}
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
			toolsSeen++
			if hiddenTools > 0 && !indicatorEmitted && toolsSeen > hiddenTools {
				indicator := fmt.Sprintf("⋯ +%d older tool calls", hiddenTools)
				b.WriteString(toolOverflowStyle.Render(indicator))
				b.WriteByte('\n')
				indicatorEmitted = true
			}
			if toolsSeen <= hiddenTools {
				continue
			}
			b.WriteString(renderToolBlock(part.Tool, innerWidth))
			b.WriteByte('\n')
		}
	}

	if f.Partial != "" {
		partial := f.Partial
		if len(partial) > innerWidth-2 {
			partial = partial[:innerWidth-2]
		}
		b.WriteString(framePartialIndicator + partial)
		b.WriteByte('\n')
	}

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
	toolPreviewLimit      = 15
)

var toolOverflowStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("240")).
	Faint(true)

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

	// Focused frame border (yellow)
	frameFocusedBorder = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("11")).
				Padding(0, 1)
)

// frameStyle returns the appropriate border style for a frame.
func frameStyle(f *Frame, focused bool) lipgloss.Style {
	switch {
	case f.Kind == ErrorFrame:
		return frameErrorBorder
	case f.Kind == PlanFrame:
		return framePlanBorder
	case f.State == FrameInProgress:
		return frameActiveBorder
	case focused:
		return frameFocusedBorder
	default:
		return frameAgentBorder
	}
}

// renderFrameStatic renders a finished frame as plain text without lipgloss borders.
// Header: "⏺ AgentID (model)" prefix. Text parts use cached MarkdownRendered.
// Tool blocks render as indented plain text, fully expanded (no toolPreviewLimit).
func renderFrameStatic(f *Frame, width int) string {
	if width < 20 {
		width = 20
	}
	var b strings.Builder

	// Header with icon prefix
	var headerParts []string
	switch f.Kind {
	case AgentFrame:
		headerParts = append(headerParts, f.AgentID)
		if f.AgentModel != "" {
			headerParts = append(headerParts, fmt.Sprintf("(%s)", f.AgentModel))
		}
	case PlanFrame:
		headerParts = append(headerParts, "Plan")
	case CompletionFrame:
		headerParts = append(headerParts, "Complete")
	case ErrorFrame:
		headerParts = append(headerParts, "Error")
	}

	label := strings.Join(headerParts, " ")
	b.WriteString("⏺ " + label)
	b.WriteByte('\n')

	// Body: text parts and tool blocks
	for _, part := range f.Parts {
		if part.IsText {
			// Use cached glamour-rendered markdown when available.
			if part.MarkdownRendered != "" {
				b.WriteString(part.MarkdownRendered)
				b.WriteByte('\n')
				continue
			}
			// Fallback: raw text with line truncation.
			text := strings.TrimRight(part.Text, "\n")
			lines := strings.Split(text, "\n")
			innerWidth := width - 2
			for _, line := range lines {
				if len(line) > innerWidth {
					line = line[:innerWidth]
				}
				b.WriteString(line)
				b.WriteByte('\n')
			}
		} else {
			// Tool block: plain indented text, no borders, fully expanded.
			b.WriteString(renderToolBlockStatic(part.Tool, width))
			b.WriteByte('\n')
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

// renderToolBlockStatic renders a tool block as plain indented text.
// Format: "  <icon> <Name> <Detail>" — no lipgloss borders.
func renderToolBlockStatic(tb ToolBlock, maxWidth int) string {
	icon := tb.Icon
	if icon == "" {
		icon = IconForAction(tb.Name)
	}
	label := fmt.Sprintf("  %s %s", icon, tb.Name)
	if tb.Detail != "" {
		label += " " + tb.Detail
	}
	innerWidth := maxWidth - 2
	if len(label) > innerWidth {
		label = label[:innerWidth]
	}
	return label
}
