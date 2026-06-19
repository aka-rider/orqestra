package tui

import (
	"image"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"
)

// --- Types ---

type transcriptLineKind uint8

const (
	transcriptLineText transcriptLineKind = iota
	transcriptLineRule                    // horizontal separator
)

// transcriptSpan is one styled run within a logical line.
type transcriptSpan struct {
	text  string
	style lipgloss.Style
}

// transcriptLine is one logical line in the transcript.
type transcriptLine struct {
	kind  transcriptLineKind
	spans []transcriptSpan
	label string // for transcriptLineRule: the label embedded in the divider
}

// transcriptPos is a logical position (rune-column, 0-based).
type transcriptPos struct {
	line, col int
}

// transcriptCell is one terminal cell for rendering.
type transcriptCell struct {
	r     rune
	w     int // visual width (1 or 2 for CJK)
	style lipgloss.Style
}

// transcriptRow is one soft-wrapped visual row derived from a logical line.
type transcriptRow struct {
	lineIdx  int              // which logical line
	startCol int              // first rune-col in this visual row
	cells    []transcriptCell // pre-built cells for this row
}

// transcriptStyles holds color configuration for the transcript.
type transcriptStyles struct {
	selectionBg string // ANSI color code for selection background (e.g. "238")
	rule        lipgloss.Style
}

// Transcript is a scrollable, mouse-selectable, auto-copying rich-text
// transcript. It is a value sub-model: callers hold copies, Update returns
// new copies plus optional tea.Cmd.
type Transcript struct {
	lines []transcriptLine
	rect  image.Rectangle // absolute terminal rect (set via SetRect)

	rows []transcriptRow // soft-wrap cache; rebuilt on Append/SetRect
	top  int             // first visible row index
	// follow: auto-scroll to bottom on append unless user scrolled or selecting.
	follow bool

	anchor, cursor transcriptPos
	selecting      bool
	hasSel         bool
	lastCopied     string

	autoscrollDir int    // -1=up, +1=down, 0=stopped
	dragSeq       uint64 // generation token for stale autoscroll ticks

	fullWidth int // terminal width for full-width rule rendering (0 = use rect Dx())

	styles transcriptStyles
}

// NewTranscript creates a transcript with the given style config.
func NewTranscript(styles transcriptStyles) Transcript {
	return Transcript{
		follow: true,
		styles: styles,
	}
}

// newTextLine creates a plain unstyled text transcript line.
func newTextLine(text string) transcriptLine {
	return transcriptLine{kind: transcriptLineText, spans: []transcriptSpan{{text: text}}}
}

// newRuleLine creates a separator rule line with the given label.
func newRuleLine(label string) transcriptLine {
	return transcriptLine{kind: transcriptLineRule, label: label}
}

// HasContent reports whether the transcript has any logical lines.
func (t Transcript) HasContent() bool { return len(t.lines) > 0 }

// --- API ---

// Append adds logical lines to the transcript. Rebuilds the display row cache
// and scrolls to bottom if follow is active.
func (t *Transcript) Append(lines ...transcriptLine) {
	t.lines = append(t.lines, lines...)
	t.rebuildRows()
	if t.follow {
		t.scrollToBottom()
	}
}

// Clear removes all content from the transcript.
func (t *Transcript) Clear() {
	t.lines = nil
	t.rows = nil
	t.top = 0
	t.follow = true
	t.hasSel = false
	t.selecting = false
	t.lastCopied = ""
}

// SetRect sets the absolute terminal rectangle that the transcript occupies.
// Must be called from an Update path (never from View).
func (t *Transcript) SetRect(r image.Rectangle) {
	pinned := t.AtBottom()
	t.rect = r
	t.rebuildRows()
	if pinned {
		t.scrollToBottom()
	}
}

// SetSize is a convenience wrapper around SetRect that takes width/height
// dimensions, keeping the top-left corner unchanged.
func (t *Transcript) SetSize(w, h int) {
	t.SetRect(image.Rect(t.rect.Min.X, t.rect.Min.Y, t.rect.Min.X+w, t.rect.Min.Y+h))
}

// ScrollToBottom scrolls to the last row and enables auto-follow.
func (t *Transcript) ScrollToBottom() {
	t.follow = true
	t.scrollToBottom()
}

// ScrollToTop scrolls to the first row and disables auto-follow.
func (t *Transcript) ScrollToTop() {
	t.follow = false
	t.top = 0
}

// SetFullWidth sets the terminal width used to render full-width rule lines.
// Call from recalculateLayout whenever the terminal width changes.
func (t *Transcript) SetFullWidth(w int) { t.fullWidth = w }

// AtBottom reports whether the last visible row is the final content row.
func (t Transcript) AtBottom() bool {
	h := t.rect.Dy()
	if h <= 0 {
		return true
	}
	return t.top+h >= len(t.rows)
}

// Selecting reports whether a drag selection is in progress.
func (t Transcript) Selecting() bool { return t.selecting }

// HasSelection reports whether a completed selection exists.
func (t Transcript) HasSelection() bool { return t.hasSel }

// SelectedText returns the selected text, joining logical lines with \n and
// skipping rule/separator lines.
func (t Transcript) SelectedText() string {
	if !t.hasSel {
		return ""
	}
	selMin, selMax := normaliseSel(t.anchor, t.cursor)
	var b strings.Builder
	for lineIdx, line := range t.lines {
		if lineIdx < selMin.line || lineIdx > selMax.line {
			continue
		}
		if line.kind == transcriptLineRule {
			continue
		}
		startCol, endCol := 0, lineRuneLen(line)
		if lineIdx == selMin.line {
			startCol = selMin.col
		}
		if lineIdx == selMax.line {
			endCol = selMax.col
		}
		text := lineSubstring(line, startCol, endCol)
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text)
	}
	return b.String()
}

// Update handles messages routed to the transcript (mouse + autoscroll ticks).
func (t Transcript) Update(msg tea.Msg) (Transcript, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.MouseMsg:
		return t.handleMouse(msg)
	case transcriptAutoscrollMsg:
		return t.handleAutoscrollTick(msg)
	case tea.KeyPressMsg:
		return t.handleKey(msg)
	}
	return t, nil
}

// handleKey handles scroll key bindings.
func (t Transcript) handleKey(msg tea.KeyPressMsg) (Transcript, tea.Cmd) {
	h := t.rect.Dy()
	switch msg.String() {
	case "pgup", "ctrl+b":
		t.follow = false
		margin := scrolloff(h)
		t.top -= max(1, h-margin)
		t.clampTop()
	case "pgdn", "ctrl+f":
		margin := scrolloff(h)
		t.top += max(1, h-margin)
		t.clampTop()
		if t.AtBottom() {
			t.follow = true
		}
	case "home", "ctrl+home":
		t.ScrollToTop()
	case "end", "ctrl+end":
		t.ScrollToBottom()
	}
	return t, nil
}

// --- Internal helpers ---

func (t *Transcript) rebuildRows() {
	w := t.rect.Dx()
	if w <= 0 {
		t.rows = nil
		return
	}
	t.rows = buildDisplayRows(t.lines, w)
}

func (t *Transcript) scrollToBottom() {
	h := t.rect.Dy()
	if h <= 0 {
		t.top = 0
		return
	}
	total := len(t.rows)
	t.top = max(0, total-h)
}

func (t *Transcript) clampTop() {
	total := len(t.rows)
	h := t.rect.Dy()
	maxTop := max(0, total-h)
	if t.top < 0 {
		t.top = 0
	}
	if t.top > maxTop {
		t.top = maxTop
	}
}

// emitCopy returns a SetClipboard command if the selected text differs from
// what was last copied (de-dupes OSC 52 spam).
func (t *Transcript) emitCopy() tea.Cmd {
	text := t.SelectedText()
	if text == "" || text == t.lastCopied {
		return nil
	}
	t.lastCopied = text
	return tea.SetClipboard(text)
}

// --- Display row builder ---

func buildDisplayRows(lines []transcriptLine, w int) []transcriptRow {
	rows := make([]transcriptRow, 0, len(lines)+16)
	for lineIdx, line := range lines {
		if line.kind == transcriptLineRule {
			rows = append(rows, transcriptRow{lineIdx: lineIdx})
			continue
		}
		cells := buildLineCells(line.spans)
		if len(cells) == 0 {
			rows = append(rows, transcriptRow{lineIdx: lineIdx})
			continue
		}
		// Word-boundary soft-wrap into rows of width w.
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
				// remainder fits on one row
				rows = append(rows, transcriptRow{lineIdx: lineIdx, startCol: startCell, cells: cells[startCell:]})
				break
			}
			// Prefer breaking at the last space; fall back to character boundary.
			// Use > (not >=) to avoid breakAt==startCell which produces an empty row.
			breakAt := i
			if lastSpace > startCell {
				breakAt = lastSpace
			}
			rows = append(rows, transcriptRow{lineIdx: lineIdx, startCol: startCell, cells: cells[startCell:breakAt]})
			// Advance past the break point, skipping any leading spaces on the next row.
			startCell = breakAt
			for startCell < len(cells) && cells[startCell].r == ' ' {
				startCell++
			}
		}
	}
	return rows
}

// buildLineCells converts spans into a flat cell slice for one logical line.
func buildLineCells(spans []transcriptSpan) []transcriptCell {
	var cells []transcriptCell
	for _, sp := range spans {
		for _, r := range sp.text {
			if r == '\n' || r == '\r' {
				continue
			}
			w := runewidth.RuneWidth(r)
			if w == 0 {
				w = 1
			}
			cells = append(cells, transcriptCell{r: r, w: w, style: sp.style})
		}
	}
	return cells
}

// --- Logical line helpers ---

// lineRuneLen returns the total rune count of a logical line's text spans.
func lineRuneLen(line transcriptLine) int {
	n := 0
	for _, sp := range line.spans {
		n += utf8.RuneCountInString(sp.text)
	}
	return n
}

// lineSubstring extracts rune [start, end) from the concatenated span text.
func lineSubstring(line transcriptLine, start, end int) string {
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

func normaliseSel(a, b transcriptPos) (transcriptPos, transcriptPos) {
	if posLessThan(a, b) {
		return a, b
	}
	return b, a
}

func posLessThan(a, b transcriptPos) bool {
	if a.line != b.line {
		return a.line < b.line
	}
	return a.col < b.col
}

func posEqual(a, b transcriptPos) bool {
	return a.line == b.line && a.col == b.col
}

func inSelectionRange(lineIdx, col int, selMin, selMax transcriptPos) bool {
	pos := transcriptPos{line: lineIdx, col: col}
	return !posLessThan(pos, selMin) && posLessThan(pos, selMax)
}

// --- Style comparison ---

func transcriptStylesEqual(a, b lipgloss.Style) bool {
	return a.Render("x") == b.Render("x")
}
