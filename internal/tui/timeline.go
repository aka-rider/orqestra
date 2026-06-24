package tui

import (
	"image"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// constToolFrameMax is the maximum number of recent tool frames shown bright.
// Older tool frames are folded into a dim "+N more tools" summary.
const constToolFrameMax = 8

// Timeline is a scrollable, mouse-selectable, append-only log of Frames.
// It replaces the separate Transcript + streamingConsole combination.
//
// Invariants:
//   - Frames are appended once; never removed (append-only).
//   - Only the live partial (liveText) mutates between renders.
//   - Static frames cache rows at the current width; a width change invalidates
//     all frame caches and triggers re-wrap.
//   - Plan frames cache rows built from markdownedit display snapshots.
//
// Value sub-model: callers hold copies; methods return updated copies.
type Timeline struct {
	lines      []timelineLine
	frames     []frame
	planFrames []planFrame // plan frame objects indexed by frame.planIdx

	// Live frame state (in-progress agent output).
	liveText string
	blinkOn  bool
	blinkTag int
	active   bool // true while an agent is running

	// Display row cache (flat, rebuilt on append or width change).
	rows []timelineRow

	// Scroll state.
	rect    image.Rectangle
	top     int
	follow  bool // auto-scroll to bottom on append

	// Scroll anchor for resize stability when plan frame heights change.
	anchorFrameIdx int
	anchorIntraRow int

	// Selection state.
	anchor, cursor timelinePos
	selecting      bool
	hasSel         bool
	lastCopied     string

	autoscrollDir int
	dragSeq       uint64
	fullWidth     int // terminal width for full-width rule rendering

	styles timelineStyles

	// Expanded controls whether tool frames beyond constToolFrameMax are shown
	// or collapsed behind a dim "+N more tools" summary.
	expanded bool
}

// NewTimeline creates a timeline with the given style config.
func NewTimeline(styles timelineStyles) Timeline {
	return Timeline{
		follow: true,
		styles: styles,
	}
}

// --- Content API (called from Update paths) ---

// Start marks the timeline active and begins the blink loop.
func (t Timeline) Start() (Timeline, tea.Cmd) {
	t.active = true
	t.blinkTag++
	return t, blinkCmd(t.blinkTag)
}

// AppendDelta accumulates a streaming text delta into the live partial.
func (t *Timeline) AppendDelta(text string) {
	t.liveText += text
}

// ClearLive discards the live partial without promoting it to a static frame.
// Used when an EntryText arrives — the completed text will be appended via
// AppendProse, so the partial (which is the same content) must not be doubled.
func (t *Timeline) ClearLive() {
	t.liveText = ""
}

// FlushLive promotes the live partial to a static Prose Frame and clears it.
// Must be called before AppendToolPending and on agent/phase transitions.
func (t *Timeline) FlushLive() {
	if t.liveText == "" {
		return
	}
	t.appendProseLine(t.liveText)
	t.liveText = ""
}

// AppendProse appends a completed prose line as a static Prose Frame.
func (t *Timeline) AppendProse(text string) {
	text = strings.TrimRight(text, "\n\r")
	if text == "" {
		return
	}
	t.appendProseLine(text)
}

func (t *Timeline) appendProseLine(text string) {
	lineIdx := len(t.lines)
	t.lines = append(t.lines, newTimelineTextLine(text))
	fIdx := len(t.frames)
	t.frames = append(t.frames, frame{
		kind:      frameKindProse,
		rawSource: text,
		lineStart: lineIdx,
		lineEnd:   lineIdx + 1,
		planIdx:   -1,
	})
	t.appendRowsForLines(lineIdx, lineIdx+1, fIdx)
	if t.follow {
		t.scrollToBottom()
	}
}

// AppendPhase appends a phase separator rule frame.
func (t *Timeline) AppendPhase(label string) {
	lineIdx := len(t.lines)
	t.lines = append(t.lines, newTimelineRuleLine(label))
	fIdx := len(t.frames)
	t.frames = append(t.frames, frame{
		kind:      frameKindPhase,
		rawSource: "",
		lineStart: lineIdx,
		lineEnd:   lineIdx + 1,
		planIdx:   -1,
	})
	t.appendRowsForLines(lineIdx, lineIdx+1, fIdx)
	if t.follow {
		t.scrollToBottom()
	}
}

// AppendToolPending appends a new Tool Frame in the pending state.
// Callers should call FlushLive first.
func (t *Timeline) AppendToolPending(text string) {
	lineIdx := len(t.lines)
	line := newTimelineToolLine(text, toolStatusPending)
	t.lines = append(t.lines, line)
	fIdx := len(t.frames)
	t.frames = append(t.frames, frame{
		kind:       frameKindTool,
		rawSource:  text,
		lineStart:  lineIdx,
		lineEnd:    lineIdx + 1,
		planIdx:    -1,
		toolStatus: toolStatusPending,
	})
	t.appendRowsForLines(lineIdx, lineIdx+1, fIdx)
	if t.follow {
		t.scrollToBottom()
	}
}

// ResolveLastTool resolves the most recent pending Tool Frame to ok or error.
func (t *Timeline) ResolveLastTool(isErr bool) {
	status := toolStatusOK
	if isErr {
		status = toolStatusErr
	}
	// Walk frames in reverse to find the last pending tool.
	for i := len(t.frames) - 1; i >= 0; i-- {
		if t.frames[i].kind == frameKindTool && t.frames[i].toolStatus == toolStatusPending {
			t.frames[i].toolStatus = status
			// Update the corresponding line's toolStatus too.
			lineIdx := t.frames[i].lineStart
			if lineIdx < len(t.lines) {
				t.lines[lineIdx].toolStatus = status
			}
			return
		}
	}
}

// ReconcilePendingTools resolves any still-pending Tool Frames to unknown.
// Must be called when an agent completes, to prevent stuck-pending frames.
func (t *Timeline) ReconcilePendingTools() {
	for i := range t.frames {
		if t.frames[i].kind == frameKindTool && t.frames[i].toolStatus == toolStatusPending {
			t.frames[i].toolStatus = toolStatusUnknown
			lineIdx := t.frames[i].lineStart
			if lineIdx < len(t.lines) {
				t.lines[lineIdx].toolStatus = toolStatusUnknown
			}
		}
	}
}

// AppendSteer appends a Steer Frame (user action: post/approve/comment/answer).
func (t *Timeline) AppendSteer(text string) {
	if text == "" {
		return
	}
	lineIdx := len(t.lines)
	display := "you: " + text
	t.lines = append(t.lines, timelineLine{
		kind:  timelineLineText,
		spans: []timelineSpan{{text: display, style: dimStyle}},
	})
	fIdx := len(t.frames)
	t.frames = append(t.frames, frame{
		kind:      frameKindSteer,
		rawSource: text,
		lineStart: lineIdx,
		lineEnd:   lineIdx + 1,
		planIdx:   -1,
	})
	t.appendRowsForLines(lineIdx, lineIdx+1, fIdx)
	if t.follow {
		t.scrollToBottom()
	}
}

// AppendAgentSummary appends an end-of-agent summary line (done/failed) as a
// static meta frame, e.g. "Done: ✓ architect (qwen3.6)  ↑236k ↓456k  3m28s".
func (t *Timeline) AppendAgentSummary(text string) {
	if text == "" {
		return
	}
	lineIdx := len(t.lines)
	t.lines = append(t.lines, timelineLine{
		kind:  timelineLineText,
		spans: []timelineSpan{{text: text, style: phaseStyle}},
	})
	fIdx := len(t.frames)
	t.frames = append(t.frames, frame{
		kind:      frameKindSteer,
		rawSource: text,
		lineStart: lineIdx,
		lineEnd:   lineIdx + 1,
		planIdx:   -1,
	})
	t.appendRowsForLines(lineIdx, lineIdx+1, fIdx)
	if t.follow {
		t.scrollToBottom()
	}
}

// AppendPlanFrame appends a Plan Frame to the Timeline.
// The plan frame must already have resize(w) called at the current width.
func (t *Timeline) AppendPlanFrame(pf planFrame) {
	pfIdx := len(t.planFrames)
	t.planFrames = append(t.planFrames, pf)
	fIdx := len(t.frames)
	f := frame{
		kind:      frameKindPlan,
		opaque:    true,
		rawSource: pf.rawMarkdown,
		lineStart: len(t.lines), // plan frames don't use lines, but mark position
		lineEnd:   len(t.lines),
		planIdx:   pfIdx,
	}
	// Attach plan frame rows with this frameIdx.
	f.rowStart = len(t.rows)
	pfRows := pf.rows()
	for _, row := range pfRows {
		t.rows = append(t.rows, timelineRow{
			lineIdx:  row.lineIdx,
			startCol: row.startCol,
			cells:    row.cells,
			opaque:   true,
			frameIdx: fIdx,
		})
	}
	f.rowEnd = len(t.rows)
	t.frames = append(t.frames, f)
	if t.follow {
		t.scrollToBottom()
	}
}

// --- Layout ---

// SetRect sets the absolute terminal rectangle. Triggers re-wrap of all plain
// frames and resize of all plan frames. Must be called from an Update path.
func (t *Timeline) SetRect(r image.Rectangle) {
	newW := r.Dx()
	oldW := t.rect.Dx()
	pinned := t.AtBottom()
	oldRect := t.rect
	t.rect = r

	if newW != oldW && newW > 0 {
		// Width changed: capture anchor, resize plan frames, rebuild rows.
		t.captureAnchor()
		for i := range t.planFrames {
			t.planFrames[i].resize(newW)
		}
		t.rebuildRows()
		if pinned {
			t.scrollToBottom()
		} else {
			t.restoreAnchor()
		}
	} else if r != oldRect {
		// Height changed only: re-clamp.
		if pinned {
			t.scrollToBottom()
		} else {
			t.clampTop()
		}
	}
}

// SetFullWidth sets the terminal width used to render full-width rule lines.
func (t *Timeline) SetFullWidth(w int) { t.fullWidth = w }

// HasContent reports whether the timeline has any frames.
func (t Timeline) HasContent() bool { return len(t.frames) > 0 || t.liveText != "" }

// AtBottom reports whether the last visible row is at or past the final content row.
func (t Timeline) AtBottom() bool {
	h := t.rect.Dy()
	if h <= 0 {
		return true
	}
	return t.top+h >= len(t.rows)
}

// ScrollToBottom scrolls to the last row and enables auto-follow.
func (t *Timeline) ScrollToBottom() {
	t.follow = true
	t.scrollToBottom()
}

// ScrollToTop scrolls to the first row and disables auto-follow.
func (t *Timeline) ScrollToTop() {
	t.follow = false
	t.top = 0
}

// Selecting reports whether a drag selection is in progress.
func (t Timeline) Selecting() bool { return t.selecting }

// HasSelection reports whether a completed selection exists.
func (t Timeline) HasSelection() bool { return t.hasSel }

// SelectedText returns the selected text.
// For opaque (plan) frames: emits the frame's raw markdown source.
// For plain frames: char-precise substring.
func (t Timeline) SelectedText() string {
	if !t.hasSel {
		return ""
	}
	selMin, selMax := normaliseTimelineSel(t.anchor, t.cursor)
	var b strings.Builder

	// Collect which opaque frames are (even partially) in the selection range.
	emittedOpaque := map[int]bool{}

	for lineIdx, line := range t.lines {
		if lineIdx < selMin.line || lineIdx > selMax.line {
			continue
		}
		fIdx := t.lineFrameIdx(lineIdx)
		f := &t.frames[fIdx]

		if f.opaque {
			// Opaque frames: emit rawSource once per frame in selection.
			if !emittedOpaque[fIdx] {
				emittedOpaque[fIdx] = true
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(f.rawSource)
			}
			continue
		}

		if line.kind == timelineLineRule {
			continue
		}

		startCol, endCol := 0, timelineLineRuneLen(line)
		if lineIdx == selMin.line {
			startCol = selMin.col
		}
		if lineIdx == selMax.line {
			endCol = selMax.col
		}
		text := timelineLineSubstring(line, startCol, endCol)
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text)
	}

	// Also emit plan frames that are selected (their lines are not in t.lines).
	for i, f := range t.frames {
		if f.kind != frameKindPlan || emittedOpaque[i] {
			continue
		}
		// Check if any of this frame's rows are in the selection row range.
		for _, row := range t.rows[f.rowStart:f.rowEnd] {
			rowPos := timelinePos{line: row.lineIdx, col: 0}
			if !timelinePosLessThan(rowPos, selMin) && timelinePosLessThan(rowPos, selMax) {
				if !emittedOpaque[i] {
					emittedOpaque[i] = true
					if b.Len() > 0 {
						b.WriteByte('\n')
					}
					b.WriteString(f.rawSource)
				}
				break
			}
		}
	}

	return b.String()
}

// CopySelected returns a SetClipboard command for the selected text.
// De-dupes if same as lastCopied.
func (t *Timeline) CopySelected() tea.Cmd {
	text := t.SelectedText()
	if text == "" || text == t.lastCopied {
		return nil
	}
	t.lastCopied = text
	return tea.SetClipboard(text)
}

// CopyFrame copies the raw source of the frame at the given row index.
func (t *Timeline) CopyFrame(rowIdx int) tea.Cmd {
	if rowIdx < 0 || rowIdx >= len(t.rows) {
		return nil
	}
	fIdx := t.rows[rowIdx].frameIdx
	if fIdx < 0 || fIdx >= len(t.frames) {
		return nil
	}
	text := t.frames[fIdx].rawSource
	if text == "" || text == t.lastCopied {
		return nil
	}
	t.lastCopied = text
	return tea.SetClipboard(text)
}

// Clear removes all content and resets to zero state.
func (t *Timeline) Clear() {
	t.lines = nil
	t.frames = nil
	t.planFrames = nil
	t.liveText = ""
	t.blinkOn = false
	t.active = false
	t.rows = nil
	t.top = 0
	t.follow = true
	t.hasSel = false
	t.selecting = false
	t.lastCopied = ""
	t.anchorFrameIdx = 0
	t.anchorIntraRow = 0
}

// Update handles messages routed to the timeline (mouse, autoscroll, blink).
func (t Timeline) Update(msg tea.Msg) (Timeline, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.MouseMsg:
		return t.handleMouse(msg)
	case timelineAutoscrollMsg:
		return t.handleAutoscrollTick(msg)
	case timelineBlinkMsg:
		if !t.active || msg.tag != t.blinkTag {
			return t, nil
		}
		t.blinkOn = !t.blinkOn
		return t, blinkCmd(t.blinkTag)
	case tea.KeyPressMsg:
		return t.handleKey(msg)
	}
	return t, nil
}

// --- Internal helpers ---

// lineFrameIdx returns the frame index for a given line index.
// This is O(frames) but frames are small in practice.
func (t *Timeline) lineFrameIdx(lineIdx int) int {
	for i := len(t.frames) - 1; i >= 0; i-- {
		f := &t.frames[i]
		if f.kind == frameKindPlan {
			continue // plan frames have lineStart==lineEnd
		}
		if lineIdx >= f.lineStart && lineIdx < f.lineEnd {
			return i
		}
	}
	return 0
}

// appendRowsForLines builds and appends rows for the given line range.
// Only used for non-plan frames.
func (t *Timeline) appendRowsForLines(lineStart, lineEnd, fIdx int) {
	w := t.rect.Dx()
	if w <= 0 {
		return
	}
	f := &t.frames[fIdx]
	f.rowStart = len(t.rows)
	for li := lineStart; li < lineEnd; li++ {
		t.rows = append(t.rows, wrapLineToRows(li, t.lines[li], w, fIdx)...)
	}
	f.rowEnd = len(t.rows)
}

// rebuildRows rebuilds the entire row cache from scratch.
// Must be called after a width change.
func (t *Timeline) rebuildRows() {
	w := t.rect.Dx()
	if w <= 0 {
		t.rows = nil
		for i := range t.frames {
			t.frames[i].rowStart = 0
			t.frames[i].rowEnd = 0
		}
		return
	}
	t.rows = make([]timelineRow, 0, len(t.rows))

	// Determine which line belongs to which frame.
	lineToFrame := make([]int, len(t.lines))
	for i := range lineToFrame {
		lineToFrame[i] = t.lineFrameIdx(i)
	}

	// Process frames in order.
	for fIdx := range t.frames {
		f := &t.frames[fIdx]
		f.rowStart = len(t.rows)
		if f.kind == frameKindPlan {
			// Plan frame: use cached rows from planFrame.
			if f.planIdx >= 0 && f.planIdx < len(t.planFrames) {
				for _, row := range t.planFrames[f.planIdx].rows() {
					t.rows = append(t.rows, timelineRow{
						lineIdx:  row.lineIdx,
						startCol: row.startCol,
						cells:    row.cells,
						opaque:   true,
						frameIdx: fIdx,
					})
				}
			}
		} else {
			// Plain frame: rebuild from lines.
			for li := f.lineStart; li < f.lineEnd; li++ {
				t.rows = append(t.rows, wrapLineToRows(li, t.lines[li], w, fIdx)...)
			}
		}
		f.rowEnd = len(t.rows)
	}
}

func (t *Timeline) scrollToBottom() {
	h := t.rect.Dy()
	if h <= 0 {
		t.top = 0
		return
	}
	total := len(t.rows)
	t.top = max(0, total-h)
}

func (t *Timeline) clampTop() {
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

// captureAnchor stores the scroll anchor as (frameIdx, intra-frame row) from
// the current top row. Used before plan frame resize to enable stable restore.
func (t *Timeline) captureAnchor() {
	if len(t.rows) == 0 || t.top < 0 {
		t.anchorFrameIdx = 0
		t.anchorIntraRow = 0
		return
	}
	idx := t.top
	if idx >= len(t.rows) {
		idx = len(t.rows) - 1
	}
	row := t.rows[idx]
	t.anchorFrameIdx = row.frameIdx
	if t.anchorFrameIdx < len(t.frames) {
		t.anchorIntraRow = idx - t.frames[t.anchorFrameIdx].rowStart
	}
}

// restoreAnchor sets top from the stored anchor after a row rebuild.
func (t *Timeline) restoreAnchor() {
	if t.anchorFrameIdx < 0 || t.anchorFrameIdx >= len(t.frames) {
		t.clampTop()
		return
	}
	f := t.frames[t.anchorFrameIdx]
	t.top = f.rowStart + t.anchorIntraRow
	t.clampTop()
}

// handleKey handles scroll key bindings.
func (t Timeline) handleKey(msg tea.KeyPressMsg) (Timeline, tea.Cmd) {
	h := t.rect.Dy()
	switch msg.String() {
	case "pgup", "ctrl+b":
		t.follow = false
		margin := scrolloff(h)
		t.top -= max(1, h-margin)
		t.clampTop()
	case "pgdown", "ctrl+f":
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
