package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// View renders the timeline for the current viewport. Pure: no side effects.
func (t Timeline) View() string {
	w := t.rect.Dx()
	h := t.rect.Dy()
	if w <= 0 || h <= 0 {
		return ""
	}

	selMin, selMax := normaliseTimelineSel(t.anchor, t.cursor)

	// Build the set of dim tool frame indices (oldest tools, beyond constToolFrameMax).
	dimToolFrames := t.dimToolFrameSet(t.expanded)

	// Count how many tool frames are dimmed (for the summary line).
	dimCount := len(dimToolFrames)

	// Track whether we've emitted the dim summary line yet.
	dimSummaryEmitted := false

	var b strings.Builder
	b.Grow(w * h * 2)

	rowsRendered := 0 // rows written to output

	for rowOffset := range h {
		rowIdx := t.top + rowOffset
		if rowIdx >= len(t.rows) {
			// Remaining rows: live frame then blanks.
			break
		}
		row := t.rows[rowIdx]
		fIdx := row.frameIdx

		// Skip / collapse dim tool frames.
		if fIdx < len(t.frames) && t.frames[fIdx].kind == frameKindTool && dimToolFrames[fIdx] {
			if !dimSummaryEmitted {
				dimSummaryEmitted = true
				summary := dimStyle.Render(fmt.Sprintf(" … and +%d more tools", dimCount))
				b.WriteString(summary)
				b.WriteByte('\n')
				rowsRendered++
			}
			continue
		}

		if row.opaque {
			// Opaque (plan frame) row: render cells as-is.
			b.WriteByte(' ')
			b.WriteString(renderTimelineCells(row, false, selMin, selMax, t.styles.selectionBg))
			b.WriteByte('\n')
			rowsRendered++
			continue
		}

		if rowIdx >= len(t.rows) || row.lineIdx >= len(t.lines) {
			b.WriteByte('\n')
			rowsRendered++
			continue
		}

		line := t.lines[row.lineIdx]

		switch line.kind {
		case timelineLineRule:
			rw := t.fullWidth
			if rw <= 0 {
				rw = w
			}
			b.WriteString(renderTimelineRule(line.label, rw, t.styles.rule))

		case timelineLineTool:
			b.WriteByte(' ')
			text := ""
			if len(line.spans) > 0 {
				text = line.spans[0].text
			}
			b.WriteString(renderToolLine(line.toolStatus, text, w-2))

		default:
			b.WriteByte(' ')
			b.WriteString(renderTimelineCells(row, t.hasSel, selMin, selMax, t.styles.selectionBg))
		}
		b.WriteByte('\n')
		rowsRendered++
	}

	// Live frame: rendered below the last static row.
	liveRowsAvail := h - rowsRendered
	if t.active && liveRowsAvail > 0 {
		liveLines := strings.Split(t.liveText, "\n")
		// Trim trailing empty strings.
		for len(liveLines) > 0 && liveLines[len(liveLines)-1] == "" {
			liveLines = liveLines[:len(liveLines)-1]
		}
		// Show last liveRowsAvail-1 lines + cursor line.
		written := 0
		if len(liveLines) > 0 {
			start := 0
			if len(liveLines) > liveRowsAvail-1 {
				start = len(liveLines) - (liveRowsAvail - 1)
			}
			for _, l := range liveLines[start:] {
				if written >= liveRowsAvail-1 {
					break
				}
				b.WriteString(streamSpeechStyle.Render(" "+truncateRunes(l, w-2)))
				b.WriteByte('\n')
				written++
			}
		}
		// Cursor line.
		cursor := streamSpeechStyle.Faint(t.blinkOn).Render("⏺")
		b.WriteString(cursor)
		b.WriteByte('\n')
		rowsRendered += written + 1
	}

	// Pad remaining rows with blank lines.
	for rowsRendered < h {
		b.WriteByte('\n')
		rowsRendered++
	}

	return b.String()
}

// dimToolFrameSet returns a set of frame indices that should be dimmed.
// When expanded is false, the most recent constToolFrameMax tool frames are bright;
// older ones are dim. When expanded is true, all tool frames are shown (nil map).
func (t Timeline) dimToolFrameSet(expanded bool) map[int]bool {
	if expanded {
		return nil
	}
	// Collect tool frame indices in order.
	var toolIdxs []int
	for i, f := range t.frames {
		if f.kind == frameKindTool {
			toolIdxs = append(toolIdxs, i)
		}
	}
	if len(toolIdxs) <= constToolFrameMax {
		return nil
	}
	dim := make(map[int]bool)
	cutoff := len(toolIdxs) - constToolFrameMax
	for _, idx := range toolIdxs[:cutoff] {
		dim[idx] = true
	}
	return dim
}

// renderToolLine renders a single tool activity line with status icon.
func renderToolLine(status toolStatus, text string, maxW int) string {
	var icon string
	var style lipgloss.Style
	switch status {
	case toolStatusPending:
		icon = "◌ "
		style = streamToolPendingStyle
	case toolStatusOK:
		icon = "✓ "
		style = streamToolOKStyle
	case toolStatusErr:
		icon = "✗ "
		style = streamToolErrStyle
	default:
		icon = "· "
		style = dimStyle
	}
	return style.Render(icon + truncateRunes(text, max(0, maxW-2)))
}

// renderTimelineRule renders a ────[ label ]──── separator at width w.
func renderTimelineRule(label string, w int, style lipgloss.Style) string {
	if label == "" {
		return style.Render(strings.Repeat("─", max(1, w)))
	}
	left := "────[ " + label + " ]"
	fill := max(0, w-lipgloss.Width(left))
	return style.Render(left + strings.Repeat("─", fill))
}

// renderTimelineCells renders one display row with a selection overlay.
func renderTimelineCells(row timelineRow, hasSel bool, selMin, selMax timelinePos, selBg string) string {
	if len(row.cells) == 0 {
		return ""
	}

	var b strings.Builder
	b.Grow(len(row.cells) * 2)

	var runBuf strings.Builder
	var runStyle lipgloss.Style
	first := true

	flush := func() {
		if runBuf.Len() > 0 {
			b.WriteString(runStyle.Render(runBuf.String()))
			runBuf.Reset()
		}
	}

	for i, c := range row.cells {
		runeCol := row.startCol + i
		eff := c.style
		if hasSel && !row.opaque && inTimelineSelectionRange(row.lineIdx, runeCol, selMin, selMax) {
			eff = c.style.Background(lipgloss.Color(selBg))
		}
		if first {
			runStyle = eff
			first = false
		}
		if !timelineStylesEqual(eff, runStyle) {
			flush()
			runStyle = eff
		}
		runBuf.WriteRune(c.r)
	}
	flush()
	return b.String()
}

// truncateRunes clips s to maxLen runes.
func truncateRunes(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen])
}

var (
	streamSpeechStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("15"))

	streamToolOKStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("2"))

	streamToolErrStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("3"))

	streamToolPendingStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("244"))
)
