package tui

import (
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"
)

// --- Frame taxonomy ---

type frameKind uint8

const (
	frameKindProse  frameKind = iota // completed prose text (one logical line)
	frameKindTool                    // tool invocation + result
	frameKindPhase                   // phase separator rule
	frameKindSteer                   // user post / approval / comment / answer
	frameKindPlan                    // plan markdown (held in planFrames slice)
)

type toolStatus uint8

const (
	toolStatusPending toolStatus = iota // awaiting result
	toolStatusOK                        // resolved: success
	toolStatusErr                       // resolved: error
	toolStatusUnknown                   // reconciled after agent-done with no result
)

// frame describes one semantic block in the Timeline.
// lineStart/lineEnd index into Timeline.lines (0 for plan frames).
// rowStart/rowEnd index into the flat Timeline.rows slice; set by rebuildRows.
type frame struct {
	kind      frameKind
	opaque    bool   // true → line-granular copy; rows come from planFrame
	rawSource string // text to copy: plain text for prose/steer; raw markdown for plan
	lineStart int    // first line index in Timeline.lines
	lineEnd   int    // one past last line (lineStart==lineEnd for plan frames)
	planIdx   int    // index into Timeline.planFrames; -1 for non-plan frames
	rowStart  int    // first row index in Timeline.rows (set by rebuildRows)
	rowEnd    int    // one past last row (set by rebuildRows)
	toolStatus toolStatus
}

// --- Line types ---

type timelineLineKind uint8

const (
	timelineLineText timelineLineKind = iota
	timelineLineRule
	timelineLineTool
)

// timelineSpan is one styled run within a logical line.
type timelineSpan struct {
	text  string
	style lipgloss.Style
}

// timelineLine is one logical line in the timeline.
type timelineLine struct {
	kind       timelineLineKind
	spans      []timelineSpan
	label      string     // for timelineLineRule
	toolStatus toolStatus // for timelineLineTool
	opaque     bool       // true = belongs to a plan frame (line-granular copy)
	frameIdx   int        // which frame owns this line
}

// --- Cell and row types ---

// timelineCell is one terminal cell for rendering.
type timelineCell struct {
	r     rune
	w     int // visual width (1 or 2 for CJK)
	style lipgloss.Style
}

// timelineRow is one soft-wrapped visual row derived from a logical line.
type timelineRow struct {
	lineIdx  int              // which logical line (in Timeline.lines)
	startCol int              // first rune-col in this visual row
	cells    []timelineCell   // pre-built cells for this row
	opaque   bool             // true = belongs to an opaque (plan) frame
	frameIdx int              // which frame owns this row
}

// timelinePos is a logical position (rune-column, 0-based).
type timelinePos struct {
	line, col int
}

// timelineStyles holds color configuration for the timeline.
type timelineStyles struct {
	selectionBg string
	rule        lipgloss.Style
}

// --- Autoscroll and blink messages ---

// timelineAutoscrollMsg drives continuous scroll while the user holds a mouse
// button beyond the timeline edge. stale ticks (seq mismatch) are no-ops.
type timelineAutoscrollMsg struct{ seq uint64 }

const timelineBlinkInterval = 500 * time.Millisecond

// timelineBlinkMsg is the tagged self-tick for the live frame blink cursor.
type timelineBlinkMsg struct{ tag int }

// blinkCmd returns the next tagged blink tick.
func blinkCmd(tag int) tea.Cmd {
	return tea.Tick(timelineBlinkInterval, func(time.Time) tea.Msg {
		return timelineBlinkMsg{tag: tag}
	})
}

// --- Helpers shared between timeline.go and timeline_view.go ---

// wrapLineToRows soft-wraps one logical line into one or more display rows.
// li is the absolute index into Timeline.lines; fIdx is the owning frame.
// Non-text lines (rule, tool) always produce exactly one row.
func wrapLineToRows(li int, line timelineLine, w int, fIdx int) []timelineRow {
	if line.kind == timelineLineRule || line.kind == timelineLineTool {
		return []timelineRow{{lineIdx: li, opaque: false, frameIdx: fIdx}}
	}
	cells := buildTimelineCells(line.spans)
	if len(cells) == 0 {
		return []timelineRow{{lineIdx: li, opaque: false, frameIdx: fIdx}}
	}
	var rows []timelineRow
	startCell := 0
	for startCell < len(cells) {
		lineW, lastSpace := 0, -1
		i := startCell
		for i < len(cells) {
			c := cells[i]
			if lineW+c.w > w && lineW > 0 {
				break
			}
			if c.r == ' ' {
				lastSpace = i
			}
			lineW += c.w
			i++
		}
		if i >= len(cells) {
			rows = append(rows, timelineRow{lineIdx: li, startCol: startCell, cells: cells[startCell:], opaque: false, frameIdx: fIdx})
			break
		}
		breakAt := i
		if lastSpace > startCell {
			breakAt = lastSpace
		}
		rows = append(rows, timelineRow{lineIdx: li, startCol: startCell, cells: cells[startCell:breakAt], opaque: false, frameIdx: fIdx})
		startCell = breakAt
		for startCell < len(cells) && cells[startCell].r == ' ' {
			startCell++
		}
	}
	return rows
}

// buildTimelineCells converts spans into a flat cell slice for one logical line.
func buildTimelineCells(spans []timelineSpan) []timelineCell {
	var cells []timelineCell
	for _, sp := range spans {
		for _, r := range sp.text {
			if r == '\n' || r == '\r' {
				continue
			}
			w := runewidth.RuneWidth(r)
			if w == 0 {
				w = 1
			}
			cells = append(cells, timelineCell{r: r, w: w, style: sp.style})
		}
	}
	return cells
}

// lineRuneLen returns the total rune count of a timeline line's text spans.
func timelineLineRuneLen(line timelineLine) int {
	n := 0
	for _, sp := range line.spans {
		n += utf8.RuneCountInString(sp.text)
	}
	return n
}

// timelineLineSubstring extracts rune [start, end) from the concatenated span text.
func timelineLineSubstring(line timelineLine, start, end int) string {
	var b strings.Builder
	col := 0
	for _, sp := range line.spans {
		for _, r := range sp.text {
			if col >= end {
				return b.String()
			}
			if col >= start {
				b.WriteRune(r)
			}
			col++
		}
	}
	return b.String()
}

// --- Selection helpers ---

func normaliseTimelineSel(a, b timelinePos) (timelinePos, timelinePos) {
	if timelinePosLessThan(a, b) {
		return a, b
	}
	return b, a
}

func timelinePosLessThan(a, b timelinePos) bool {
	if a.line != b.line {
		return a.line < b.line
	}
	return a.col < b.col
}

func timelinePosEqual(a, b timelinePos) bool {
	return a.line == b.line && a.col == b.col
}

func inTimelineSelectionRange(lineIdx, col int, selMin, selMax timelinePos) bool {
	pos := timelinePos{line: lineIdx, col: col}
	return !timelinePosLessThan(pos, selMin) && timelinePosLessThan(pos, selMax)
}

// timelineStylesEqual reports whether two lipgloss styles render identically.
func timelineStylesEqual(a, b lipgloss.Style) bool {
	return a.Render("x") == b.Render("x")
}

// newTimelineTextLine creates a plain unstyled timeline line.
func newTimelineTextLine(text string) timelineLine {
	return timelineLine{kind: timelineLineText, spans: []timelineSpan{{text: text}}}
}

// newTimelineRuleLine creates a separator rule line.
func newTimelineRuleLine(label string) timelineLine {
	return timelineLine{kind: timelineLineRule, label: label}
}

// newTimelineToolLine creates a tool activity line.
func newTimelineToolLine(text string, status toolStatus) timelineLine {
	return timelineLine{
		kind:       timelineLineTool,
		spans:      []timelineSpan{{text: text}},
		toolStatus: status,
	}
}
