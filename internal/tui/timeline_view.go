package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/xiii/orqestra/internal/tui/frame"
)

// View renders the timeline for the current viewport. Pure: no side effects.
func (t Timeline) View() string {
	w, h := t.rect.Dx(), t.rect.Dy()
	if w <= 0 || h <= 0 {
		return ""
	}

	selMin, selMax := normaliseSel(t.anchor, t.cursor)

	var b strings.Builder
	b.Grow(w * h * 2)
	rowsRendered := 0

	for off := range h {
		ri := t.top + off
		if ri >= len(t.rows) {
			break
		}
		rr := t.rows[ri]
		b.WriteString(renderRow(rr, ri, t.hasSel, selMin, selMax, t.styles.selectionBg))
		b.WriteByte('\n')
		rowsRendered++
	}

	// Live tail renders below static rows — the Timeline asks only for Rows().
	if avail := h - rowsRendered; t.tail != nil && avail > 0 {
		rowsRendered += t.renderTail(&b, avail)
	}

	for rowsRendered < h {
		b.WriteByte('\n')
		rowsRendered++
	}
	return b.String()
}

// renderTail writes the bottom-anchored last `avail` rows of the live tail.
func (t Timeline) renderTail(b *strings.Builder, avail int) int {
	rows := t.tail.Rows()
	start := 0
	if len(rows) > avail {
		start = len(rows) - avail
	}
	written := 0
	for _, r := range rows[start:] {
		b.WriteString(renderCells(r.Cells, nil, ""))
		b.WriteByte('\n')
		written++
	}
	return written
}

// renderRow renders one static display row, overlaying the selection background
// on cells inside [selMin, selMax).
func renderRow(rr rowRef, rowIdx int, hasSel bool, selMin, selMax selPos, selBg string) string {
	var selected func(col int) bool
	if hasSel {
		selected = func(col int) bool { return inSel(rowIdx, col, selMin, selMax) }
	}
	return renderCells(rr.cells, selected, selBg)
}

// renderCells renders a flat cell slice, coalescing adjacent cells of equal
// style. When selected(col) is non-nil and true, the selection background is
// overlaid on that cell. Shared by the static rows and the live tail.
func renderCells(cells []frame.Cell, selected func(col int) bool, selBg string) string {
	if len(cells) == 0 {
		return ""
	}
	var b, runBuf strings.Builder
	var runStyle lipgloss.Style
	first := true
	flush := func() {
		if runBuf.Len() > 0 {
			b.WriteString(runStyle.Render(runBuf.String()))
			runBuf.Reset()
		}
	}
	for i, c := range cells {
		eff := c.Style
		if selected != nil && selected(i) {
			eff = c.Style.Background(lipgloss.Color(selBg))
		}
		if first {
			runStyle = eff
			first = false
		}
		if !stylesEqual(eff, runStyle) {
			flush()
			runStyle = eff
		}
		runBuf.WriteRune(c.R)
	}
	flush()
	return b.String()
}

// stylesEqual reports whether two styles render identically.
func stylesEqual(a, b lipgloss.Style) bool { return a.Render("x") == b.Render("x") }

var (
	streamSpeechStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))

	streamToolOKStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))

	streamToolErrStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))

	streamToolPendingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
)
