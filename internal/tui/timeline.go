package tui

import (
	"image"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/xiii/orqestra/internal/tui/frame"
	"github.com/xiii/orqestra/internal/tui/keymap"
)

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
	frames []frame.StaticFrame

	// tail is the single in-progress unit — a frame.InteractiveFrame that
	// Resolve()s into a static frame when complete. The Timeline forwards blink
	// ticks to it and renders its Rows() below the static list.
	tail frame.InteractiveFrame

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

	keys   keymap.Bindings
	styles timelineStyles
}

// NewTimeline creates a timeline with the given bindings and styles. Frames are
// built by callers and handed to Append — the Timeline never constructs them.
func NewTimeline(keys keymap.Bindings, styles timelineStyles) Timeline {
	return Timeline{follow: true, keys: keys, styles: styles}
}

// --- Content API (called from Update paths) ---

// Start marks the timeline active and begins the blink loop. The producer sets
// the live tail (SetTail) so the Timeline never constructs a frame.
func (t Timeline) Start() (Timeline, tea.Cmd) {
	t.active = true
	t.blinkTag++
	return t, blinkCmd(t.blinkTag)
}

// Stop ends the live tail: bumps the blink tag so any in-flight tick is ignored
// and the loop stops rescheduling, then drops the tail so the ⏺ cursor is gone
// (the fix for the eternal blink). Completed prose is already static by then.
func (t *Timeline) Stop() {
	t.active = false
	t.blinkTag++
	t.tail = nil
}

// SetTail sets the single in-progress unit (an InteractiveFrame the producer
// builds — typically the live prose with its ⏺ cursor) and lays it out. The
// Timeline forwards ticks and deltas to it but never constructs or inspects it.
func (t *Timeline) SetTail(f frame.InteractiveFrame) {
	t.tail = f.SetWidth(t.contentWidth())
}

// AppendDelta forwards a streamed chunk to the live tail. Generic: the tail
// reacts (LiveProse accumulates); other interactive frames may ignore it.
func (t *Timeline) AppendDelta(text string) {
	if t.tail != nil {
		t.tail, _ = t.tail.Update(frame.DeltaMsg{Text: text})
	}
}

// ClearLive drops the live tail (e.g. before promoting a finished turn).
func (t *Timeline) ClearLive() { t.tail = nil }


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
		if t.tail != nil {
			t.tail = t.tail.SetWidth(newW)
		}
		fallthrough
	case r != oldRect:
		if pinned {
			t.scrollToBottom()
		} else {
			t.clampTop()
		}
	}
}

// HasContent reports whether the timeline has any frames or a live tail.
func (t Timeline) HasContent() bool { return len(t.frames) > 0 || t.tail != nil }

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
	t.rows = nil
	t.tail = nil
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
		if t.tail != nil {
			t.tail, _ = t.tail.Update(frame.BlinkMsg{})
		}
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
// Append adds any StaticFrame to the timeline and returns its index. This is the
// single content entry point: the Timeline does not know or care which concrete
// frame it is — the caller builds it (NewProse, NewPhase, NewSteer, NewAnswer,
// NewPlan, NewTool, …). A frame that opts into frame.Collapsible is tracked so
// the viewport can fold old ones. New frame kinds need no new Timeline method.
// Append adds any StaticFrame to the timeline and returns its index. The
// Timeline is frame-agnostic — callers build frames (NewProse, NewPhase, NewSteer,
// NewAnswer, NewPlan, NewTool, TurnSnapshot, …) and hand them in here.
func (t *Timeline) Append(f frame.StaticFrame) int {
	f = f.SetWidth(t.contentWidth())
	idx := len(t.frames)
	t.frames = append(t.frames, f)
	t.appendRows(idx, f)
	if t.follow {
		t.scrollToBottom()
	}
	return idx
}

// PromoteTail resolves the live tail into a static frame and appends it to the
// timeline, then clears the tail. A no-op when the tail is nil or resolves to
// an empty frame. Called when a turn ends to convert an in-progress TurnGroup
// into a permanent TurnSnapshot.
func (t *Timeline) PromoteTail() {
	if t.tail == nil {
		return
	}
	static := t.tail.Resolve()
	t.tail = nil
	if static != nil && len(static.Rows()) > 0 {
		t.Append(static)
	}
}

// SetFrame replaces the frame at idx and rebuilds the row cache — used by the
// producer to resolve a pending frame in place (e.g. a tool to ok/err) without
// the Timeline knowing what kind it is.
func (t *Timeline) SetFrame(idx int, f frame.StaticFrame) {
	if idx < 0 || idx >= len(t.frames) {
		return
	}
	t.frames[idx] = f.SetWidth(t.contentWidth())
	t.rebuildRows()
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
