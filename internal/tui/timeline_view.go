package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// View renders the timeline for the current viewport. Pure: no side effects.
func (t Timeline) View() string {
	w, h := t.rect.Dx(), t.rect.Dy()
	if w <= 0 || h <= 0 {
		return ""
	}

	selMin, selMax := normaliseSel(t.anchor, t.cursor)
	dim := t.dimToolFrames()
	dimCount := len(dim)
	dimEmitted := false

	var b strings.Builder
	b.Grow(w * h * 2)
	rowsRendered := 0

	for off := range h {
		ri := t.top + off
		if ri >= len(t.rows) {
			break
		}
		rr := t.rows[ri]
		if dim[rr.frameIdx] {
			if !dimEmitted {
				dimEmitted = true
				b.WriteString(dimStyle.Render(fmt.Sprintf(" … and +%d more tools", dimCount)))
				b.WriteByte('\n')
				rowsRendered++
			}
			continue
		}
		b.WriteString(renderRow(rr, ri, t.hasSel, selMin, selMax, t.styles.selectionBg))
		b.WriteByte('\n')
		rowsRendered++
	}

	// Live tail: the in-progress prose with a blinking cursor, below static rows.
	if avail := h - rowsRendered; t.active && avail > 0 {
		rowsRendered += t.renderLive(&b, w, avail)
	}

	for rowsRendered < h {
		b.WriteByte('\n')
		rowsRendered++
	}
	return b.String()
}

// renderLive writes up to avail rows of live text plus the cursor line, and
// reports how many rows it wrote.
func (t Timeline) renderLive(b *strings.Builder, w, avail int) int {
	lines := strings.Split(t.liveText, "\n")
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	written := 0
	if len(lines) > 0 {
		start := 0
		if len(lines) > avail-1 {
			start = len(lines) - (avail - 1)
		}
		for _, l := range lines[start:] {
			if written >= avail-1 {
				break
			}
			b.WriteString(streamSpeechStyle.Render(" " + truncateRunes(l, w-2)))
			b.WriteByte('\n')
			written++
		}
	}
	b.WriteString(streamSpeechStyle.Faint(t.blinkOn).Render("⏺"))
	b.WriteByte('\n')
	return written + 1
}

// dimToolFrames returns the set of tool-frame indices to collapse (oldest tools
// beyond constToolFrameMax), or nil when expanded or under the cap.
func (t Timeline) dimToolFrames() map[int]bool {
	if t.expanded || len(t.toolIdx) <= constToolFrameMax {
		return nil
	}
	dim := make(map[int]bool)
	for _, fi := range t.toolIdx[:len(t.toolIdx)-constToolFrameMax] {
		dim[fi] = true
	}
	return dim
}

// renderRow renders one display row, overlaying the selection background on
// cells inside [selMin, selMax). Adjacent cells of equal style are coalesced.
func renderRow(rr rowRef, rowIdx int, hasSel bool, selMin, selMax selPos, selBg string) string {
	cells := rr.cells
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
		if hasSel && inSel(rowIdx, i, selMin, selMax) {
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
	streamSpeechStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))

	streamToolOKStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))

	streamToolErrStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))

	streamToolPendingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
)
