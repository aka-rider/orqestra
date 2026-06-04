package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// renderFrame renders a single frame using borderless plain-text layout.
// Both finished and in-progress frames use the same chrome-free style
// matching the Claude Code aesthetic: icon prefix + plain body, no lipgloss borders.
func renderFrame(f *Frame, width int, animFrame int, _ bool) string {
	if f.State == FrameFinished {
		return renderFrameStatic(f, width)
	}
	return renderFrameActive(f, width, animFrame)
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
	framePartialIndicator = "▎"
	toolPreviewLimit      = 15
)

var toolOverflowStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("240")).
	Faint(true)

var planHintStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("240")).
	Faint(true)

// renderFrameActive renders an in-progress frame as plain text without lipgloss borders.
// Header: "✻ AgentID (model)" with spinning character.
// Tool blocks: plain indented text (same as static), fully expanded when ToolsExpanded.
// Partial text shown with ▎ prefix at the bottom.
func renderFrameActive(f *Frame, width int, animFrame int) string {
	if width < 20 {
		width = 20
	}
	var b strings.Builder

	spin := "✻"
	if len(spinningFrames) > 0 {
		spin = spinningFrames[animFrame%len(spinningFrames)]
	}

	switch f.Kind {
	case AgentFrame:
		label := f.AgentID
		if f.AgentModel != "" {
			label += " (" + f.AgentModel + ")"
		}
		b.WriteString(spin + " " + label)
	case PlanFrame:
		b.WriteString(spin + " Plan")
	case CompletionFrame:
		b.WriteString(spin + " Complete")
	case ErrorFrame:
		b.WriteString(spin + " Error")
	}
	b.WriteByte('\n')

	if f.State == FrameInProgress && f.StreamingCollapsed {
		for _, part := range f.Parts {
			if !part.IsText {
				continue
			}
			text := strings.TrimRight(part.Text, "\n")
			lines := strings.Split(text, "\n")
			for _, line := range lines {
				if line == "" {
					continue
				}
				innerWidth := width - 2
				if len(line) > innerWidth {
					line = line[:innerWidth]
				}
				b.WriteString(" " + line)
				b.WriteByte('\n')
				break
			}
			break
		}
		return strings.TrimRight(b.String(), "\n")
	}

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

	toolsSeen := 0
	indicatorEmitted := false
	innerWidth := width - 2
	if innerWidth < 10 {
		innerWidth = 10
	}

	for _, part := range f.Parts {
		if part.IsText {
			text := strings.TrimRight(part.Text, "\n")
			lines := strings.Split(text, "\n")
			for _, line := range lines {
				if len(line) > innerWidth {
					line = line[:innerWidth]
				}
				b.WriteString(" " + line)
				b.WriteByte('\n')
			}
		} else {
			toolsSeen++
			if hiddenTools > 0 && !indicatorEmitted && toolsSeen > hiddenTools {
				b.WriteString(toolOverflowStyle.Render(fmt.Sprintf("⎿ ⋯ +%d older tool calls", hiddenTools)))
				b.WriteByte('\n')
				indicatorEmitted = true
			}
			if toolsSeen <= hiddenTools {
				continue
			}
			b.WriteString(renderToolBlockPlain(part.Tool, width))
			b.WriteByte('\n')
		}
	}

	if f.Partial != "" {
		partial := f.Partial
		if len(partial) > innerWidth-2 {
			partial = partial[:innerWidth-2]
		}
		b.WriteString(" " + framePartialIndicator + partial)
		b.WriteByte('\n')
	}

	if f.Kind == PlanFrame && f.State == FrameInProgress {
		b.WriteByte('\n')
		b.WriteString(planHintStyle.Render("[^A] accept  [Enter] comment  [^E] edit  [^D] diff"))
		b.WriteByte('\n')
	}

	return strings.TrimRight(b.String(), "\n")
}

// renderToolBlockPlain renders a tool invocation as plain indented text.
// Format: "  <icon> <Name> <Detail>" — no lipgloss borders.
func renderToolBlockPlain(tb ToolBlock, maxWidth int) string {
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

// renderFrameStatic renders a finished frame as plain text without lipgloss borders.
// Header: "⏺ AgentID (model)" prefix. Text parts use cached MarkdownRendered.
// Tool blocks render as plain indented text, fully expanded (no toolPreviewLimit).
func renderFrameStatic(f *Frame, width int) string {
	if width < 20 {
		width = 20
	}
	var b strings.Builder

	switch f.Kind {
	case AgentFrame:
		label := f.AgentID
		if f.AgentModel != "" {
			label += " (" + f.AgentModel + ")"
		}
		b.WriteString("⏺ " + label)
	case PlanFrame:
		b.WriteString("⏺ Plan")
	case CompletionFrame:
		b.WriteString("⏺ Complete")
	case ErrorFrame:
		b.WriteString("⏺ Error")
	}
	b.WriteByte('\n')

	innerWidth := width - 2
	if innerWidth < 10 {
		innerWidth = 10
	}

	for _, part := range f.Parts {
		if part.IsText {
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
				b.WriteString(" " + line)
				b.WriteByte('\n')
			}
		} else {
			b.WriteString(renderToolBlockPlain(part.Tool, width))
			b.WriteByte('\n')
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

// renderToolBlockStatic renders a tool block as plain indented text.
// Format: "  <icon> <Name> <Detail>" — no lipgloss borders.
// Delegates to renderToolBlockPlain for consistent rendering.
func renderToolBlockStatic(tb ToolBlock, maxWidth int) string {
	return renderToolBlockPlain(tb, maxWidth)
}
