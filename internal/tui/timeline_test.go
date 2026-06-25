package tui

import (
	"image"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/xiii/orqestra/internal/tui/frame"
	"github.com/xiii/orqestra/internal/tui/keymap"
)

func newTestTimeline(w, h int) Timeline {
	t := NewTimeline(keymap.Default(), timelineStyles{selectionBg: "#264F78"})
	t.SetRect(image.Rect(0, 0, w, h))
	return t
}

// --- AppendProse ---

func TestTimeline_AppendProse_AddsFrame(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.Append(frame.NewProse("hello world"))
	if !tl.HasContent() {
		t.Fatal("expected content after AppendProse")
	}
	if len(tl.frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(tl.frames))
	}
	if !strings.Contains(tl.View(), "hello world") {
		t.Error("View should contain the prose text")
	}
}

func TestTimeline_AppendProse_StripTrailingNewline(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.Append(frame.NewProse("line\n"))
	if len(tl.frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(tl.frames))
	}
	// A stripped trailing newline yields a single content row, not a blank one.
	if got := frame.Height(tl.frames[0]); got != 1 {
		t.Errorf("expected 1 row, got %d", got)
	}
}

// --- AppendPhase ---

func TestTimeline_AppendPhase_AddsRuleFrame(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.Append(frame.NewPhase("researcher: claude-opus", dividerStyle))
	if len(tl.frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(tl.frames))
	}
	view := tl.View()
	if !strings.Contains(view, "researcher: claude-opus") || !strings.Contains(view, "─") {
		t.Error("phase frame should render a labelled rule")
	}
}

// --- Generic frames: Append returns an index, SetFrame resolves in place ---
// (tool-specific resolution lives in the producer, not the Timeline)

func TestTimeline_AppendReturnsIndex_SetFrameResolves(t *testing.T) {
	tl := newTestTimeline(80, 20)
	idx := tl.Append(frame.NewTool("Read /foo/bar.go", frame.ToolStyles{}))
	if tl.CollapsibleCount() != 1 {
		t.Fatalf("expected 1 collapsible frame, got %d", tl.CollapsibleCount())
	}
	if !strings.Contains(tl.View(), "◌") {
		t.Error("pending tool should render ◌")
	}
	tl.SetFrame(idx, frame.NewTool("Read /foo/bar.go", frame.ToolStyles{}).WithStatus(frame.ToolOK))
	if !strings.Contains(tl.View(), "✓") {
		t.Error("resolved tool should render ✓")
	}
}

func TestTimeline_SetFrame_Error(t *testing.T) {
	tl := newTestTimeline(80, 20)
	idx := tl.Append(frame.NewTool("cat /etc/shadow", frame.ToolStyles{}))
	tl.SetFrame(idx, frame.NewTool("cat /etc/shadow", frame.ToolStyles{}).WithStatus(frame.ToolErr))
	if !strings.Contains(tl.View(), "✗") {
		t.Error("errored tool should render ✗")
	}
}

// --- AppendSteer ---

func TestTimeline_AppendSteer(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.Append(frame.NewSteer("approved plan", dimStyle))
	if len(tl.frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(tl.frames))
	}
	if !strings.Contains(tl.View(), "you: approved plan") {
		t.Error("steer frame should render as 'you: …'")
	}
}

// --- Live tail (AppendDelta / StartLive / ClearLive) ---

func TestTimeline_LiveTail_AppendAndClear(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendDelta("partial ")
	tl.AppendDelta("text")
	if !tl.HasContent() {
		t.Fatal("a live tail should count as content")
	}
	if !strings.Contains(tl.View(), "partial text") {
		t.Error("the live tail should render the streamed text")
	}
	tl.ClearLive()
	if tl.tail != nil {
		t.Error("ClearLive should drop the tail")
	}
}

// StartLive shows the ⏺ heartbeat (an empty prose tail) before any text streams.
func TestTimeline_StartLive_ShowsHeartbeat(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.StartLive()
	if !strings.Contains(tl.View(), "⏺") {
		t.Error("StartLive should show the ⏺ cursor even with no text")
	}
}

// --- HasContent / Clear ---

func TestTimeline_Clear(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.Append(frame.NewProse("some content"))
	tl.AppendDelta("live")
	tl.Clear()
	if tl.HasContent() {
		t.Error("after Clear, HasContent should be false")
	}
	if tl.tail != nil {
		t.Error("Clear should reset the tail")
	}
	if len(tl.rows) != 0 {
		t.Error("Clear should reset rows")
	}
}

// --- AtBottom / ScrollToBottom / ScrollToTop ---

func TestTimeline_AutoFollow(t *testing.T) {
	tl := newTestTimeline(80, 5)
	for i := range 20 {
		tl.Append(frame.NewProse(strings.Repeat("x", i+1)))
	}
	if !tl.AtBottom() {
		t.Errorf("expected AtBottom after auto-follow, top=%d rows=%d h=%d", tl.top, len(tl.rows), tl.rect.Dy())
	}
}

func TestTimeline_ScrollToTop(t *testing.T) {
	tl := newTestTimeline(80, 5)
	for range 20 {
		tl.Append(frame.NewProse("line"))
	}
	tl.ScrollToTop()
	if tl.top != 0 {
		t.Errorf("ScrollToTop: expected top=0, got %d", tl.top)
	}
	if tl.follow {
		t.Error("ScrollToTop should disable follow")
	}
}

func TestTimeline_ScrollToBottom(t *testing.T) {
	tl := newTestTimeline(80, 5)
	for range 20 {
		tl.Append(frame.NewProse("line"))
	}
	tl.ScrollToTop()
	tl.ScrollToBottom()
	if !tl.AtBottom() {
		t.Errorf("ScrollToBottom: not at bottom (top=%d rows=%d h=%d)", tl.top, len(tl.rows), tl.rect.Dy())
	}
}

// --- View output ---

func TestTimeline_View_ShowsLiveCursor(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendDelta("x") // a live prose tail carries the ⏺ cursor
	if !strings.Contains(tl.View(), "⏺") {
		t.Error("a live tail should render the ⏺ cursor")
	}
}

func TestTimeline_View_LiveDeltaText(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendDelta("partial output here")
	if !strings.Contains(tl.View(), "partial output here") {
		t.Error("View should show live delta text")
	}
}

func TestTimeline_View_DimCollapse(t *testing.T) {
	tl := newTestTimeline(80, 40)
	for i := range constToolFrameMax + 3 {
		text := strings.Repeat("x", i+1)
		idx := tl.Append(frame.NewTool(text, frame.ToolStyles{}))
		tl.SetFrame(idx, frame.NewTool(text, frame.ToolStyles{}).WithStatus(frame.ToolOK))
	}
	if !strings.Contains(tl.View(), "more tools") {
		t.Error("expected the dim-collapse summary for excess collapsible frames")
	}
}

// --- Scroll key handling (via the central keymap) ---

func TestTimeline_ScrollKeys(t *testing.T) {
	tl := newTestTimeline(80, 5)
	for range 30 {
		tl.Append(frame.NewProse("a long line to force scrolling"))
	}
	tl.ScrollToTop()
	topBefore := tl.top

	tl, _ = tl.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if tl.top <= topBefore {
		t.Errorf("pgdn: top should increase from %d, got %d", topBefore, tl.top)
	}
	topAfter := tl.top
	tl, _ = tl.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if tl.top >= topAfter {
		t.Errorf("pgup: top should decrease from %d, got %d", topAfter, tl.top)
	}
}

// --- Blink lifecycle ---

func TestTimeline_BlinkMsg_ReschedulesWhileActive(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.active = true
	tl.blinkTag = 1
	tl.AppendDelta("x") // a tail to forward the blink to
	_, cmd := tl.Update(timelineBlinkMsg{tag: 1})
	if cmd == nil {
		t.Error("a valid blink tick should reschedule the blink loop")
	}
}

func TestTimeline_BlinkMsg_StaleTagIgnored(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.active = true
	tl.blinkTag = 5
	_, cmd := tl.Update(timelineBlinkMsg{tag: 3}) // stale
	if cmd != nil {
		t.Error("a stale blink tick should not reschedule")
	}
}

// Stop halts the blink loop: after Stop, an in-flight tick is ignored and
// nothing reschedules — the fix for the cursor that blinked forever.
func TestTimeline_Stop_HaltsBlink(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl, _ = tl.Start()
	tagBefore := tl.blinkTag
	tl.Stop()
	if tl.active {
		t.Error("Stop should clear active")
	}
	if tl.blinkTag == tagBefore {
		t.Error("Stop should bump the blink tag so in-flight ticks are stale")
	}
	_, cmd := tl.Update(timelineBlinkMsg{tag: tagBefore})
	if cmd != nil {
		t.Error("a stale tick after Stop must not reschedule the blink")
	}
}

// --- Selection (visual row/col over rendered cells) ---

func TestTimeline_SelectedText_SingleLine(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.Append(frame.NewProse("hello world"))
	tl.hasSel = true
	tl.anchor = selPos{row: 0, col: 6}
	tl.cursor = selPos{row: 0, col: 11}
	if got := tl.SelectedText(); got != "world" {
		t.Errorf("SelectedText = %q, want %q", got, "world")
	}
}

func TestTimeline_SelectedText_Empty(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.Append(frame.NewProse("hello"))
	tl.hasSel = false
	if tl.SelectedText() != "" {
		t.Error("SelectedText should be empty when no selection")
	}
}

// --- Row rebuild on resize ---

func TestTimeline_Resize_StableRowCount(t *testing.T) {
	tl := newTestTimeline(80, 20)
	for range 10 {
		tl.Append(frame.NewProse("short"))
	}
	rowsBefore := len(tl.rows)
	tl.SetRect(image.Rect(0, 0, 40, 20))
	if len(tl.rows) < rowsBefore {
		t.Errorf("after narrowing, rows decreased: %d -> %d", rowsBefore, len(tl.rows))
	}
}

func TestTimeline_Resize_PreservesContent(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.Append(frame.NewProse("this line is visible"))
	tl.SetRect(image.Rect(0, 0, 40, 20))
	if !strings.Contains(tl.View(), "this line is visible") {
		t.Error("resize should not destroy prose content")
	}
}

// --- Multiple frame types together ---

func TestTimeline_MixedFrames(t *testing.T) {
	tl := newTestTimeline(80, 40)
	tl.Append(frame.NewPhase("researcher", dividerStyle))
	tl.Append(frame.NewProse("Here is my research."))
	idx := tl.Append(frame.NewTool("read /tmp/data.json", frame.ToolStyles{}))
	tl.SetFrame(idx, frame.NewTool("read /tmp/data.json", frame.ToolStyles{}).WithStatus(frame.ToolOK))
	tl.Append(frame.NewSteer("approved plan", dimStyle))

	if len(tl.frames) != 4 {
		t.Fatalf("expected 4 frames, got %d", len(tl.frames))
	}
	view := tl.View()
	for _, want := range []string{"researcher", "Here is my research.", "✓", "you: approved plan"} {
		if !strings.Contains(view, want) {
			t.Errorf("mixed view missing %q:\n%s", want, view)
		}
	}
}
