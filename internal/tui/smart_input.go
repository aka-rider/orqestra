package tui

import (
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"
)

// --- Segment types ---

// segmentKind identifies the variant of a Segment.
type segmentKind uint8

const (
	segmentText segmentKind = iota
	segmentPaste
)

// Segment is one element of the smart input's content model.
type Segment interface {
	Kind() segmentKind
	// Assembled returns the raw text that contributes to Value().
	Assembled() string
	// PillLabel is the short label shown as a pill in edit mode.
	PillLabel() string
}

// textSegment holds plain typed text.
type textSegment struct {
	text string
}

func (t textSegment) Kind() segmentKind      { return segmentText }
func (t textSegment) Assembled() string       { return t.text }
func (textSegment) PillLabel() string         { return "" }

// pasteSegment holds pasted content displayed as a pill.
type pasteSegment struct {
	label   string // e.g. "Pasted text #1"
	content string // full stored content
	lines   int    // line count for the "+N lines" suffix
}

func (p pasteSegment) Kind() segmentKind   { return segmentPaste }
func (p pasteSegment) Assembled() string   { return p.content }
func (p pasteSegment) PillLabel() string   { return p.label + " +" + intStr(p.lines) + " lines" }

// --- SmartInput ---

// SmartInput is a purpose-built multi-segment input component for the prompt
// screen. Ordinary typed text is rendered verbatim; large pastes (> 7 lines)
// appear as compact pills whose full content is included in the assembled value.
type SmartInput struct {
	segments []Segment
	cursor   int // position in the assembled value (rune index)

	width   int // available rendering width
	height  int // available height (for line-count capping)
	charLim int // max assembled length (default 4096)
	pasteN  int // next paste segment number
	blinkOn bool
	blinkCmd tea.Cmd
	err     error
}

// NewSmartInput creates an empty smart input ready for use.
func NewSmartInput() SmartInput {
	return SmartInput{
		charLim: 4096,
	}
}

// Value returns the full assembled prompt text (all segments concatenated).
// This is what the LLM receives.
func (s SmartInput) Value() string {
	var b strings.Builder
	for _, seg := range s.segments {
		b.WriteString(seg.Assembled())
	}
	return b.String()
}

// Len returns the number of runes in the assembled value.

// WithValue returns a copy of the smart input with all segments replaced
// by a single text segment containing the given value. Used by the file
// picker to replace the current content.
func (s SmartInput) WithValue(v string) SmartInput {
	si := NewSmartInput()
	si.insertSegAtCursor(textSegment{text: v})
	si.pasteN = s.pasteN
	return si
}
func (s SmartInput) Len() int {
	return utf8.RuneCountInString(s.Value())
}

// Reset clears all segments and state.

// blinkTickMsg fires periodically to toggle cursor visibility.
type blinkTickMsg struct{}
func (s *SmartInput) Reset() {
	s.segments = nil
	s.cursor = 0
	s.pasteN = 0
	s.err = nil
}

// InsertText inserts text at the given assembled-value position, shifting
// existing segments as needed. This is used by the file picker to inject
// a selected path at the cursor.
func (s *SmartInput) InsertText(pos int, text string) {
	if text == "" {
		return
	}
	segIdx, _ := s.splitAtPos(pos)

	newSegs := make([]Segment, 0, len(s.segments)+1)
	newSegs = append(newSegs, s.segments[:segIdx]...)
	newSegs = append(newSegs, textSegment{text: text})
	newSegs = append(newSegs, s.segments[segIdx:]...)
	s.segments = newSegs
	s.cursor += utf8.RuneCountInString(text)
}

// --- Update ---

// Update handles input messages for the smart input.
func (s SmartInput) Update(msg tea.Msg) (SmartInput, tea.Cmd) {
	// Handle paste: intercept before forwarding.
	if _, ok := msg.(tea.PasteMsg); ok {
		return s.handlePaste(msg.(tea.PasteMsg))
	}

	// Handle blink tick.
	if _, ok := msg.(blinkTickMsg); ok && s.blinkCmd != nil {
		s.blinkOn = !s.blinkOn
		return s, s.blinkCmd
	}

	// Handle key press.
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return s, nil
	}

	return s.handleKey(keyMsg)
}

func (s SmartInput) handlePaste(msg tea.PasteMsg) (SmartInput, tea.Cmd) {
	content := string(msg.Content)
	if content == "" {
		return s, nil
	}

	lines := countLines(content)
	if lines <= pasteThreshold {
		// Small paste: insert as text at cursor.
		s.insertSegAtCursor(textSegment{text: content})
		return s, nil
	}

	// Large paste: create a paste segment (pill).
	s.pasteN++
	ps := pasteSegment{
		label:   "Pasted text #" + intStr(s.pasteN),
		content: content,
		lines:   lines,
	}

	// Check for duplicate: if the last segment is a paste with identical content,
	// expand it instead of creating a new pill.
	if lastIdx := len(s.segments) - 1; lastIdx >= 0 {
		if prev, ok := s.segments[lastIdx].(pasteSegment); ok && prev.content == content {
			// Expand: replace pill with its full content as text.
			s.segments[lastIdx] = textSegment{text: content}
			s.cursor = s.posOfSeg(lastIdx) + utf8.RuneCountInString(content)
			return s, nil
		}
	}

	s.insertSegAtCursor(ps)
	return s, nil
}

func (s SmartInput) handleKey(msg tea.KeyPressMsg) (SmartInput, tea.Cmd) {
	switch msg.String() {
	case "shift+enter", "alt+enter":
		s.insertSegAtCursor(textSegment{text: "\n"})
		return s, nil
	case "ctrl+v":
		// Trigger clipboard read — bubbletea will emit tea.PasteMsg.
		return s, nil
	}

	switch msg.Code {
	case tea.KeyBackspace:
		return s.handleBackspace()
	case tea.KeyDelete:
		return s.handleDelete()
	case tea.KeyLeft:
		return s.handleLeft()
	case tea.KeyRight:
		return s.handleRight()
	default:
		if len(msg.Text) == 1 {
			s.insertSegAtCursor(textSegment{text: msg.Text})
			return s, nil
		}
	}

	return s, nil
}

func (s SmartInput) handleBackspace() (SmartInput, tea.Cmd) {
	if s.cursor <= 0 {
		return s, nil
	}

	segIdx := s.segmentIndexAtPos(s.cursor - 1)
	seg := s.segments[segIdx]

	switch seg.Kind() {
	case segmentPaste:
		ps := seg.(pasteSegment)
		segStart := s.posOfSeg(segIdx)
		segEnd := segStart + utf8.RuneCountInString(ps.content)

		if s.cursor >= segEnd {
			// Cursor at or after: delete the whole segment.
			s.segments = append(s.segments[:segIdx], s.segments[segIdx+1:]...)
		} else {
			// Cursor inside: move to start and delete.
			s.cursor = segStart
			s.segments = append(s.segments[:segIdx], s.segments[segIdx+1:]...)
		}
		return s, nil

	case segmentText:
		ts := seg.(textSegment)
		runes := []rune(ts.text)
		off := s.cursor - s.posOfSeg(segIdx)

		if off == 0 {
			// Cursor at start: delete last char of previous segment if any.
			if segIdx > 0 {
				prev := s.segments[segIdx-1]
				prevRunes := []rune(prev.Assembled())
				if len(prevRunes) > 0 {
					prevRunes = prevRunes[:len(prevRunes)-1]
					s.segments[segIdx-1] = textSegment{text: string(prevRunes)}
					s.cursor--
				}
			}
			return s, nil
		}

		// Delete char before cursor within this segment.
		runes = runes[:off-1]
		runes = append(runes, runes[off-1:]...)
		s.segments[segIdx] = textSegment{text: string(runes)}
		s.cursor--
		return s, nil
	}

	return s, nil
}

func (s SmartInput) handleDelete() (SmartInput, tea.Cmd) {
	if s.cursor >= s.Len() {
		return s, nil
	}

	segIdx := s.segmentIndexAtPos(s.cursor)
	seg := s.segments[segIdx]

	switch seg.Kind() {
	case segmentPaste:
		ps := seg.(pasteSegment)
		segStart := s.posOfSeg(segIdx)
		segEnd := segStart + utf8.RuneCountInString(ps.content)

		if s.cursor <= segStart {
			// Cursor is before the segment: delete the whole segment.
			s.segments = append(s.segments[:segIdx], s.segments[segIdx+1:]...)
		} else if s.cursor < segEnd {
			// Cursor inside: delete from cursor to end of segment.
			runes := []rune(ps.content)
			off := s.cursor - segStart
			if off < len(runes) {
				after := pasteSegment{
					label:   ps.label,
					content: string(runes[off:]),
					lines:   countLines(string(runes[off:])),
				}
				s.segments[segIdx] = textSegment{text: string(runes[:off])}
				newSegs := make([]Segment, 0, len(s.segments)+1)
				newSegs = append(newSegs, s.segments[:segIdx+1]...)
				newSegs = append(newSegs, after)
				newSegs = append(newSegs, s.segments[segIdx+1:]...)
				s.segments = newSegs
			}
		}
		return s, nil

	case segmentText:
		ts := seg.(textSegment)
		runes := []rune(ts.text)
		off := s.cursor - s.posOfSeg(segIdx)
		if off < len(runes) {
			runes = append(runes[:off], runes[off+1:]...)
			s.segments[segIdx] = textSegment{text: string(runes)}
		}
		return s, nil
	}

	return s, nil
}

func (s SmartInput) handleLeft() (SmartInput, tea.Cmd) {
	if s.cursor <= 0 {
		return s, nil
	}

	segIdx := s.segmentIndexAtPos(s.cursor)
	seg := s.segments[segIdx]

	switch seg.Kind() {
	case segmentPaste:
		segStart := s.posOfSeg(segIdx)

		if s.cursor > segStart {
			// Inside paste: move to start.
			s.cursor = segStart
		} else {
			// At start of paste: move into previous segment.
			s.cursor--
		}
		return s, nil

	case segmentText:
		s.cursor--
		return s, nil
	}

	return s, nil
}

func (s SmartInput) handleRight() (SmartInput, tea.Cmd) {
	total := s.Len()
	if s.cursor >= total {
		return s, nil
	}

	segIdx := s.segmentIndexAtPos(s.cursor)
	seg := s.segments[segIdx]

	switch seg.Kind() {
	case segmentPaste:
		ps := seg.(pasteSegment)
		segStart := s.posOfSeg(segIdx)
		segEnd := segStart + utf8.RuneCountInString(ps.content)

		if s.cursor < segEnd {
			// Inside paste: move to end.
			s.cursor = segEnd
		} else {
			// At end: move into next segment.
			s.cursor++
		}
		return s, nil

	case segmentText:
		s.cursor++
		return s, nil
	}

	return s, nil
}

// --- Insertion helper ---

// insertSegAtCursor inserts a segment at the cursor position.
func (s *SmartInput) insertSegAtCursor(seg Segment) {
	total := s.Len()
	if s.cursor > total {
		s.cursor = total
	}
	if s.cursor < 0 {
		s.cursor = 0
	}

	if len(s.segments) == 0 {
		s.segments = []Segment{seg}
		s.cursor = utf8.RuneCountInString(seg.Assembled())
		return
	}

	segIdx, offset := s.splitAtPos(s.cursor)

	// Split the segment at segIdx at the given offset.
	newSegs := make([]Segment, 0, len(s.segments)+1)
	newSegs = append(newSegs, s.segments[:segIdx]...)

	switch s.segments[segIdx].Kind() {
	case segmentText:
		ts := s.segments[segIdx].(textSegment)
		runes := []rune(ts.text)
		if offset <= len(runes) {
			before := string(runes[:offset])
			after := string(runes[offset:])
			if before != "" {
				newSegs = append(newSegs, textSegment{text: before})
			}
			if after != "" {
				newSegs = append(newSegs, textSegment{text: after})
			}
		}
	case segmentPaste:
		ps := s.segments[segIdx].(pasteSegment)
		runes := []rune(ps.content)
		if offset <= len(runes) {
			before := string(runes[:offset])
			afterContent := string(runes[offset:])
			if before != "" {
				newSegs = append(newSegs, textSegment{text: before})
			}
			if afterContent != "" {
				newSegs = append(newSegs, pasteSegment{
					label:   ps.label,
					content: afterContent,
					lines:   countLines(afterContent),
				})
			}
		}
	}

	// Insert the new segment.
	newSegs = append(newSegs, seg)

	// Append remaining segments.
	newSegs = append(newSegs, s.segments[segIdx+1:]...)
	s.segments = newSegs

	s.cursor += utf8.RuneCountInString(seg.Assembled())
}

// splitAtPos returns (segIdx, offset) where the assembled value should be
// split at the given position. offset is the rune index within that segment.
func (s SmartInput) splitAtPos(pos int) (segIdx, offset int) {
	if pos <= 0 {
		return 0, 0
	}
	if pos >= s.Len() {
		segIdx = len(s.segments) - 1
		if segIdx < 0 {
			return 0, 0
		}
		offset = utf8.RuneCountInString(s.segments[segIdx].Assembled())
		return segIdx, offset
	}

	accum := 0
	for i, seg := range s.segments {
		segLen := utf8.RuneCountInString(seg.Assembled())
		if pos < accum+segLen {
			return i, pos - accum
		}
		accum += segLen
	}
	return len(s.segments) - 1, 0
}

// --- Rendering ---

// View renders the smart input for the given terminal dimensions.
func (s SmartInput) View(width, height int) string {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}

	s.width = width
	s.height = height

	var b strings.Builder

	// Calculate desired input height.
	lineCount := s.desiredLineCount(width)
	inputH := min(max(constPromptInputHeight-2, lineCount), height/2-2)
	if inputH < 1 {
		inputH = 1
	}

	// Build visual rows.
	rows := s.buildRows(width)
	for i, row := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(row)
	}

	// Pad to input height.
	renderedLines := len(rows)
	for renderedLines < inputH {
		b.WriteByte('\n')
		renderedLines++
	}

	return b.String()
}

// buildRows constructs the visual rows for the input area, including cursor.
func (s SmartInput) buildRows(maxW int) []string {
	if maxW <= 0 {
		maxW = 80
	}

	// Build a flat list of (text, visualWidth) runs.
	var runs []run
	for _, seg := range s.segments {
		switch seg.Kind() {
		case segmentText:
			ts := seg.(textSegment)
			runs = append(runs, run{text: ts.text, w: runewidth.StringWidth(ts.text)})
		case segmentPaste:
			ps := seg.(pasteSegment)
			label := "[" + ps.PillLabel() + "]"
			runs = append(runs, run{text: label, w: runewidth.StringWidth(label), pill: true})
		}
	}

	// Build visual rows by wrapping runs.
	var rows []string
	var curRow []run
	var rowW int
	cursorX, cursorY := s.cursorScreenPos(maxW)

	for _, r := range runs {
		// Check if adding this run exceeds width.
		if rowW+r.w > maxW && len(curRow) > 0 {
			rows = append(rows, renderRow(curRow, cursorX, cursorY, rowW, len(rows), maxW))
			curRow = nil
			rowW = 0
			cursorX = 0
			cursorY++
		}
		curRow = append(curRow, r)
		rowW += r.w
	}

	// Finalize last row.
	if len(curRow) > 0 {
		rows = append(rows, renderRow(curRow, cursorX, cursorY, rowW, len(rows), maxW))
	} else {
		rows = append(rows, renderRow(nil, cursorX, cursorY, 0, len(rows), maxW))
	}

	return rows
}

// run is a text or pill unit within a row.
type run struct {
	text string
	w    int
	pill bool
}

// renderRow renders a row of runs with the cursor marker.
func renderRow(runs []run, cursorX, cursorY, rowW, rowIdx, maxW int) string {
	var b strings.Builder

	// If cursor is at the start of this row, render it.
	if cursorX == 0 && cursorY == rowIdx {
		b.WriteRune('█')
	}

	for _, r := range runs {
		if r.pill {
			b.WriteString(pillStyle.Render(r.text))
		} else {
			b.WriteString(r.text)
		}
	}

	// Pad to full width.
	currentW := 0
	if cursorX == 0 && cursorY == rowIdx {
		currentW++
	}
	for _, r := range runs {
		currentW += r.w
	}
	if currentW < maxW {
		b.WriteString(strings.Repeat(" ", maxW-currentW))
	}

	return b.String()
}

// cursorScreenPos returns the (x, y) screen position of the cursor.
func (s SmartInput) cursorScreenPos(maxW int) (x, y int) {
	if maxW <= 0 {
		maxW = 80
	}

	x = 0
	y = 0

	for _, seg := range s.segments {
		switch seg.Kind() {
		case segmentText:
			ts := seg.(textSegment)
			runes := []rune(ts.text)
			segLen := len(runes)

			if s.cursor >= x && s.cursor < x+segLen {
				// Cursor is inside this text segment.
				off := s.cursor - x
				vw := 0
				for i := 0; i < off; i++ {
					vw += runewidth.RuneWidth(runes[i])
				}
				if vw >= maxW {
					y++
					x = vw - maxW
				} else {
					x = vw
				}
				return x, y
			}

			// Count visual rows consumed by this segment.
			vw := 0
			for _, r := range runes {
				vw += runewidth.RuneWidth(r)
				if vw > maxW {
					y++
					vw = 0
				}
			}
			if vw > 0 {
				x = vw
			}
			if vw >= maxW && len(runes) > 0 {
				y++
				x = 0
			}

		case segmentPaste:
			ps := seg.(pasteSegment)
			segLen := utf8.RuneCountInString(ps.content)

			if s.cursor >= x && s.cursor < x+segLen {
				// Cursor snapped to start of paste segment.
				return x, y
			}
			x += segLen
			// Pills count as 1 visual row.
			if x > maxW {
				y++
				x = 0
			}
		}
	}

	// Cursor is at end.
	return s.endScreenPos(maxW)
}

func (s SmartInput) endScreenPos(maxW int) (x, y int) {
	x = 0
	y = 0
	for _, seg := range s.segments {
		switch seg.Kind() {
		case segmentText:
			ts := seg.(textSegment)
			runes := []rune(ts.text)
			vw := 0
			for _, r := range runes {
				vw += runewidth.RuneWidth(r)
				if vw > maxW {
					y++
					vw = 0
				}
			}
			if vw > 0 {
				x = vw
			}
			if vw >= maxW && len(runes) > 0 {
				y++
				x = 0
			}
		case segmentPaste:
			x = 0
			y++
		}
	}
	return x, y
}

// desiredLineCount returns the number of rendered lines.
func (s SmartInput) desiredLineCount(termWidth int) int {
	if termWidth <= 0 {
		return 3
	}

	count := 0
	for _, seg := range s.segments {
		switch seg.Kind() {
		case segmentText:
			ts := seg.(textSegment)
			count += countRenderedLines(ts.text, termWidth)
		case segmentPaste:
			count += 1
		}
	}
	return count
}

// --- Position helpers ---

// segmentIndexAtPos returns the segment index that contains the given position.
func (s SmartInput) segmentIndexAtPos(pos int) int {
	if pos <= 0 || len(s.segments) == 0 {
		return 0
	}
	if pos >= s.Len() {
		return len(s.segments) - 1
	}

	accum := 0
	for i, seg := range s.segments {
		segLen := utf8.RuneCountInString(seg.Assembled())
		if pos < accum+segLen {
			return i
		}
		accum += segLen
	}
	return len(s.segments) - 1
}

// posOfSeg returns the assembled-value position of the start of a segment.
func (s SmartInput) posOfSeg(idx int) int {
	accum := 0
	for i := 0; i < idx; i++ {
		accum += utf8.RuneCountInString(s.segments[i].Assembled())
	}
	return accum
}

// --- Line counting ---

func countLines(s string) int {
	if s == "" {
		return 1
	}
	n := 0
	for _, r := range s {
		if r == '\n' {
			n++
		}
	}
	return n + 1
}

func countRenderedLines(text string, maxW int) int {
	if maxW <= 0 {
		return 1
	}
	if text == "" {
		return 1
	}

	lines := strings.Split(text, "\n")
	count := 0
	for _, line := range lines {
		w := runewidth.StringWidth(line)
		if w == 0 {
			count++
		} else {
			count += (w + maxW - 1) / maxW
		}
	}
	return count
}

// --- Utility ---

func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

func intStr(n int) string {
	if n == 0 {
		return "0"
	}
	const digits = "0123456789"
	var b strings.Builder
	for n > 0 {
		b.WriteByte(digits[n%10])
		n /= 10
	}
	// Reverse.
	for i, j := 0, b.Len()-1; i < j; i, j = i+1, j-1 {
		b.WriteByte(b.String()[j])
		b.WriteByte(b.String()[i])
	}
	return b.String()
}

// pillStyle renders a paste pill with the accent color scheme.
var pillStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("0")).
	Background(lipgloss.Color("12")).
	Bold(true).
	MarginRight(1)

// pasteThreshold is the maximum number of lines for inline pasting.
const pasteThreshold = 7
