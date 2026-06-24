package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// --- Selection geometry (visual: flat row index + rune column) ---

func normaliseSel(a, b selPos) (selPos, selPos) {
	if lessSel(a, b) {
		return a, b
	}
	return b, a
}

func lessSel(a, b selPos) bool {
	if a.row != b.row {
		return a.row < b.row
	}
	return a.col < b.col
}

// inSel reports whether (row, col) lies in the half-open range [selMin, selMax).
func inSel(row, col int, selMin, selMax selPos) bool {
	p := selPos{row: row, col: col}
	return !lessSel(p, selMin) && lessSel(p, selMax)
}

// Selecting reports whether a drag selection is in progress.
func (t Timeline) Selecting() bool { return t.selecting }

// HasSelection reports whether a completed selection exists.
func (t Timeline) HasSelection() bool { return t.hasSel }

// SelectedText returns the rendered text inside the current selection.
func (t Timeline) SelectedText() string {
	if !t.hasSel {
		return ""
	}
	selMin, selMax := normaliseSel(t.anchor, t.cursor)
	var b strings.Builder
	for r := selMin.row; r <= selMax.row && r < len(t.rows); r++ {
		cells := t.rows[r].cells
		start, end := 0, len(cells)
		if r == selMin.row {
			start = selMin.col
		}
		if r == selMax.row {
			end = selMax.col
		}
		start, end = clamp(start, 0, len(cells)), clamp(end, 0, len(cells))
		if start > end {
			start = end
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		for _, c := range cells[start:end] {
			b.WriteRune(c.R)
		}
	}
	return b.String()
}

// CopySelected returns a SetClipboard command for the selection, de-duped.
func (t *Timeline) CopySelected() tea.Cmd {
	text := t.SelectedText()
	if text == "" || text == t.lastCopied {
		return nil
	}
	t.lastCopied = text
	return tea.SetClipboard(text)
}

// CopyFrame copies the full rendered text of the frame owning the given row.
func (t *Timeline) CopyFrame(rowIdx int) tea.Cmd {
	if rowIdx < 0 || rowIdx >= len(t.rows) {
		return nil
	}
	fi := t.rows[rowIdx].frameIdx
	var b strings.Builder
	for _, rr := range t.rows {
		if rr.frameIdx != fi {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		for _, c := range rr.cells {
			b.WriteRune(c.R)
		}
	}
	text := b.String()
	if text == "" || text == t.lastCopied {
		return nil
	}
	t.lastCopied = text
	return tea.SetClipboard(text)
}
