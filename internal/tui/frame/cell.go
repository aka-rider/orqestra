// Package frame holds the Timeline's transcript items as small, polymorphic
// models. Each item is a StaticFrame that knows how to lay itself out into
// display rows at a given width; the Timeline owns scrolling, selection, and
// the single live tail (an InteractiveFrame). There is no frameKind enum and
// no container switching on a tag — adding an item type means adding a type,
// not a case (open/closed).
package frame

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"
)

// Cell is one terminal cell: a rune, its display width (1, or 2 for CJK/emoji),
// and the style it renders with.
type Cell struct {
	R     rune
	W     int
	Style lipgloss.Style
}

// Row is one fully wrapped display row — a flat list of cells.
type Row struct {
	Cells []Cell
}

// Span is a styled run of text used to build a frame's cells before wrapping.
type Span struct {
	Text  string
	Style lipgloss.Style
}

// Text returns the plain runes of a row (no styling) — the unit of copy.
func (r Row) Text() string {
	var b strings.Builder
	b.Grow(len(r.Cells))
	for _, c := range r.Cells {
		b.WriteRune(c.R)
	}
	return b.String()
}

// Width returns the total display width of a row in cells.
func (r Row) Width() int {
	w := 0
	for _, c := range r.Cells {
		w += c.W
	}
	return w
}

// truncate clips s to at most n runes (display columns are not counted; tool
// lines are ASCII-dominant, matching the prior renderer).
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// truncateToTail keeps the last n runes of s (i.e., right-truncates from the
// left). Used for tool details where the basename is more useful than the path prefix.
func truncateToTail(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[len(runes)-n:])
}

// cellsFromSpans flattens styled spans into a single cell slice, dropping any
// newline/carriage-return runes (wrapping is by width, not by embedded newline).
func cellsFromSpans(spans []Span) []Cell {
	var cells []Cell
	for _, sp := range spans {
		for _, r := range sp.Text {
			if r == '\n' || r == '\r' {
				continue
			}
			w := runewidth.RuneWidth(r)
			if w == 0 {
				w = 1
			}
			cells = append(cells, Cell{R: r, W: w, Style: sp.Style})
		}
	}
	return cells
}

// wrapCells soft-wraps a flat cell slice into rows at display width w, breaking
// on the last space before the limit when possible (word wrap), else hard-
// breaking. A non-positive width yields a single unwrapped row. Empty input
// yields one empty row so a blank line still occupies vertical space.
func wrapCells(cells []Cell, w int) []Row {
	if len(cells) == 0 {
		return []Row{{}}
	}
	if w <= 0 {
		return []Row{{Cells: cells}}
	}
	var rows []Row
	start := 0
	for start < len(cells) {
		lineW, lastSpace := 0, -1
		i := start
		for i < len(cells) {
			c := cells[i]
			if lineW+c.W > w && lineW > 0 {
				break
			}
			if c.R == ' ' {
				lastSpace = i
			}
			lineW += c.W
			i++
		}
		if i >= len(cells) {
			rows = append(rows, Row{Cells: cells[start:]})
			break
		}
		breakAt := i
		if lastSpace > start {
			breakAt = lastSpace
		}
		rows = append(rows, Row{Cells: cells[start:breakAt]})
		start = breakAt
		for start < len(cells) && cells[start].R == ' ' {
			start++
		}
	}
	return rows
}
