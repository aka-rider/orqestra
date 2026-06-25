package tui

import (
	"image"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/xiii/orqestra/internal/tui/frame"
	"github.com/xiii/orqestra/internal/tui/keymap"
)

// constToolFrameMax is the number of recent tool frames shown bright; older
// ones fold into a dim "+N more tools" summary unless expanded.
const constToolFrameMax = 8

// timelineStyles holds the Timeline's own colors. Per-frame styling lives on
// the frames themselves — the Timeline only owns the selection background.
type timelineStyles struct {
	selectionBg string
}

// rowRef is one wrapped display row plus the index of the frame it came from.
// The Timeline's flat row cache is the single coordinate space for scrolling
// and selection — there is no logical-line layer.
type rowRef struct {
	cells    []frame.Cell
	frameIdx int
}

// selPos is a visual selection endpoint: a flat row index and a rune column.
type selPos struct{ row, col int }

// Timeline is a scrollable, mouse-selectable log of StaticFrames plus one live
// tail (the in-progress prose). It owns scrolling and selection; it does not
// know how any frame renders itself — each frame lays itself out into rows.
//
// Value sub-model: callers hold copies; methods return updated copies.
type Timeline struct {
	frames  []frame.StaticFrame
	toolIdx []int // indices into frames that hold a frame.Tool

	// Live tail (in-progress prose). Only this blinks. Stop() clears it on run
	// end so the blink tick stops rescheduling — the fix for the eternal cursor.
	liveText string
	blinkOn  bool
	blinkTag int
	active   bool

	// Flat display-row cache, rebuilt on append or width change.
	rows []rowRef

	// Scroll.
	rect   image.Rectangle
	top    int
	follow bool

	// Selection (visual: flat row index + rune column).
	anchor, cursor selPos
	selecting      bool
	hasSel         bool
	lastCopied     string

	autoscrollDir int
	dragSeq       uint64

	keys     keymap.Bindings
	styles   timelineStyles
	expanded bool
}

// NewTimeline creates a timeline with the given bindings and styles. Frames are
// built by callers and handed to Append — the Timeline never constructs them.
func NewTimeline(keys keymap.Bindings, styles timelineStyles) Timeline {
	return Timeline{follow: true, keys: keys, styles: styles}
}

// --- Content API (called from Update paths) ---

// Start marks the timeline active and begins the blink loop.
func (t Timeline) Start() (Timeline, tea.Cmd) {
	t.active = true
	t.blinkTag++
	return t, blinkCmd(t.blinkTag)
}

// Stop ends the live tail: clears active and bumps the blink tag so any
// in-flight blink tick is ignored and the loop stops rescheduling.
func (t *Timeline) Stop() {
	t.active = false
	t.blinkOn = false
	t.blinkTag++
}

// AppendDelta accumulates a streaming text delta into the live partial.
func (t *Timeline) AppendDelta(text string) { t.liveText += text }

// ClearLive discards the live partial without promoting it.
func (t *Timeline) ClearLive() { t.liveText = "" }

// FlushLive promotes the live partial to a Prose frame and clears it.
func (t *Timeline) FlushLive() {
	if t.liveText == "" {
		return
	}
	t.Append(frame.NewProse(t.liveText))
	t.liveText = ""
}

// AppendToolPending appends a new pending Tool frame and tracks it so a later
// result can resolve it. The pending → ok/err transition is live tool state the
// Timeline owns; callers FlushLive first. Plain content frames go through Append.
func (t *Timeline) AppendToolPending(text string) {
	t.toolIdx = append(t.toolIdx, len(t.frames))
	t.Append(frame.NewTool(text, toolFrameStyles()))
}

// ResolveLastTool resolves the most recent pending Tool frame to ok or error.
func (t *Timeline) ResolveLastTool(isErr bool) {
	status := frame.ToolOK
	if isErr {
		status = frame.ToolErr
	}
	for i := len(t.toolIdx) - 1; i >= 0; i-- {
		fi := t.toolIdx[i]
		tool, ok := t.frames[fi].(frame.Tool)
		if ok && tool.Status() == frame.ToolPending {
			t.frames[fi] = tool.WithStatus(status)
			t.rebuildRows()
			return
		}
	}
}

// ReconcilePendingTools resolves any still-pending Tool frames to unknown.
func (t *Timeline) ReconcilePendingTools() {
	changed := false
	for _, fi := range t.toolIdx {
		tool, ok := t.frames[fi].(frame.Tool)
		if ok && tool.Status() == frame.ToolPending {
			t.frames[fi] = tool.WithStatus(frame.ToolUnknown)
			changed = true
		}
	}
	if changed {
		t.rebuildRows()
	}
}

// ToolCount reports the number of tool frames (for footer overflow hints).
func (t Timeline) ToolCount() int { return len(t.toolIdx) }

// --- Layout & scroll ---

// SetRect sets the absolute terminal rectangle, re-wrapping all frames on a
// width change. Must be called from an Update path.
func (t *Timeline) SetRect(r image.Rectangle) {
	newW, oldW := r.Dx(), t.rect.Dx()
	pinned := t.AtBottom()
	oldRect := t.rect
	t.rect = r
	switch {
	case newW != oldW && newW > 0:
		t.rebuildRows()
		fallthrough
	case r != oldRect:
		if pinned {
			t.scrollToBottom()
		} else {
			t.clampTop()
		}
	}
}

// HasContent reports whether the timeline has any frames or live text.
func (t Timeline) HasContent() bool { return len(t.frames) > 0 || t.liveText != "" }

// AtBottom reports whether the viewport is at or past the final row.
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

// Clear removes all content and resets to zero state.
func (t *Timeline) Clear() {
	t.frames = nil
	t.toolIdx = nil
	t.rows = nil
	t.liveText = ""
	t.blinkOn = false
	t.active = false
	t.top = 0
	t.follow = true
	t.hasSel = false
	t.selecting = false
	t.lastCopied = ""
}

// Update handles messages routed to the timeline (mouse, autoscroll, blink, keys).
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

// handleKey handles scroll bindings.
func (t Timeline) handleKey(msg tea.KeyPressMsg) (Timeline, tea.Cmd) {
	h := t.rect.Dy()
	switch {
	case key.Matches(msg, t.keys.PageUp):
		t.follow = false
		t.top -= max(1, h-scrolloff(h))
		t.clampTop()
	case key.Matches(msg, t.keys.PageDown):
		t.top += max(1, h-scrolloff(h))
		t.clampTop()
		if t.AtBottom() {
			t.follow = true
		}
	case key.Matches(msg, t.keys.ScrollTop):
		t.ScrollToTop()
	case key.Matches(msg, t.keys.ScrollBottom):
		t.ScrollToBottom()
	}
	return t, nil
}

// --- Internal row management ---

// contentWidth is the width frames lay out to.
func (t Timeline) contentWidth() int { return t.rect.Dx() }

// appendStatic lays out a frame at the current width, stores it, and appends its
// rows to the flat cache.
// Append adds any StaticFrame to the timeline. This is the single content entry
// point: the Timeline does not know or care which concrete frame it is — the
// caller builds it (NewProse, NewPhase, NewSteer, NewAnswer, NewPlan, …). New
// frame kinds need no new Timeline method.
func (t *Timeline) Append(f frame.StaticFrame) {
	f = f.SetWidth(t.contentWidth())
	idx := len(t.frames)
	t.frames = append(t.frames, f)
	t.appendRows(idx, f)
	if t.follow {
		t.scrollToBottom()
	}
}

func (t *Timeline) appendRows(idx int, f frame.StaticFrame) {
	for _, r := range f.Rows() {
		t.rows = append(t.rows, rowRef{cells: r.Cells, frameIdx: idx})
	}
}

// rebuildRows re-lays-out every frame at the current width and rebuilds the row
// cache. Called after a width change or a tool-status change.
func (t *Timeline) rebuildRows() {
	w := t.contentWidth()
	t.rows = t.rows[:0]
	if w <= 0 {
		return
	}
	for i := range t.frames {
		t.frames[i] = t.frames[i].SetWidth(w)
		t.appendRows(i, t.frames[i])
	}
}

func (t *Timeline) scrollToBottom() {
	h := t.rect.Dy()
	if h <= 0 {
		t.top = 0
		return
	}
	t.top = max(0, len(t.rows)-h)
}

func (t *Timeline) clampTop() {
	maxTop := max(0, len(t.rows)-t.rect.Dy())
	t.top = max(0, min(t.top, maxTop))
}
