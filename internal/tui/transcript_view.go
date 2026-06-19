package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// View renders the transcript for the current viewport. Pure: no side effects,
// no mutations, no locking.
func (t Transcript) View() string {
	w := t.rect.Dx()
	h := t.rect.Dy()
	if w <= 0 || h <= 0 {
		return ""
	}

	selMin, selMax := normaliseSel(t.anchor, t.cursor)

	var b strings.Builder
	b.Grow(w * h * 2)

	for rowOffset := range h {
		rowIdx := t.top + rowOffset
		if rowIdx >= len(t.rows) {
			b.WriteByte('\n')
			continue
		}
		row := t.rows[rowIdx]
		if row.lineIdx >= len(t.lines) {
			b.WriteByte('\n')
			continue
		}
		line := t.lines[row.lineIdx]

		if line.kind == transcriptLineRule {
			rw := t.fullWidth
			if rw <= 0 {
				rw = w
			}
			b.WriteString(renderTranscriptRule(line.label, rw, t.styles.rule))
		} else {
			b.WriteByte(' ')
			b.WriteString(renderTranscriptCells(row, t.hasSel, selMin, selMax, t.styles.selectionBg))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// renderTranscriptRule renders a ────[ label ]──── separator at width w.
func renderTranscriptRule(label string, w int, style lipgloss.Style) string {
	if label == "" {
		return style.Render(strings.Repeat("─", max(1, w)))
	}
	left := "────[ " + label + " ]"
	fill := max(0, w-lipgloss.Width(left))
	return style.Render(left + strings.Repeat("─", fill))
}

// renderTranscriptCells renders one display row with a selection overlay.
// Selected cells keep their foreground colour and gain the selection background.
func renderTranscriptCells(row transcriptRow, hasSel bool, selMin, selMax transcriptPos, selBg string) string {
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
		if hasSel && inSelectionRange(row.lineIdx, runeCol, selMin, selMax) {
			eff = c.style.Background(lipgloss.Color(selBg))
		}
		if first {
			runStyle = eff
			first = false
		}
		if !transcriptStylesEqual(eff, runStyle) {
			flush()
			runStyle = eff
		}
		runBuf.WriteRune(c.r)
	}
	flush()
	return b.String()
}
