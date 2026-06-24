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
	t := NewTimeline(keymap.Default(), timelineStyles{selectionBg: "#264F78", rule: dividerStyle}, frame.MDDeps{})
	t.SetRect(image.Rect(0, 0, w, h))
	return t
}

// --- AppendProse ---

func TestTimeline_AppendProse_AddsFrame(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendProse("hello world")
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

func TestTimeline_AppendProse_EmptySkipped(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendProse("")
	if tl.HasContent() {
		t.Error("empty prose should not produce frames")
	}
}

func TestTimeline_AppendProse_StripTrailingNewline(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendProse("line\n")
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
	tl.AppendPhase("researcher: claude-opus")
	if len(tl.frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(tl.frames))
	}
	view := tl.View()
	if !strings.Contains(view, "researcher: claude-opus") || !strings.Contains(view, "─") {
		t.Error("phase frame should render a labelled rule")
	}
}

// --- AppendToolPending / ResolveLastTool ---

func TestTimeline_ToolPendingAndResolve(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendToolPending("Read /foo/bar.go")
	if len(tl.frames) != 1 || tl.ToolCount() != 1 {
		t.Fatalf("expected 1 tool frame, got frames=%d tools=%d", len(tl.frames), tl.ToolCount())
	}
	if !strings.Contains(tl.View(), "◌") {
		t.Error("pending tool should render ◌")
	}
	tl.ResolveLastTool(false)
	if !strings.Contains(tl.View(), "✓") {
		t.Error("resolved tool should render ✓")
	}
}

func TestTimeline_ResolveLastTool_Error(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendToolPending("cat /etc/shadow")
	tl.ResolveLastTool(true)
	if !strings.Contains(tl.View(), "✗") {
		t.Error("errored tool should render ✗")
	}
}

func TestTimeline_ResolveLastTool_MultipleTools(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendToolPending("tool 1")
	tl.ResolveLastTool(false)
	tl.AppendToolPending("tool 2")
	tl.ResolveLastTool(true)
	view := tl.View()
	if !strings.Contains(view, "✓") || !strings.Contains(view, "✗") {
		t.Errorf("expected both ✓ (tool 1) and ✗ (tool 2) in view:\n%s", view)
	}
}

// --- ReconcilePendingTools ---

func TestTimeline_ReconcilePendingTools(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendToolPending("tool 1")
	tl.AppendToolPending("tool 2")
	tl.ReconcilePendingTools()
	view := tl.View()
	if strings.Contains(view, "◌") {
		t.Error("no tool should remain pending after reconcile")
	}
	if !strings.Contains(view, "·") {
		t.Error("reconciled tools should render the unknown icon ·")
	}
}

// --- AppendSteer ---

func TestTimeline_AppendSteer(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendSteer("approved plan")
	if len(tl.frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(tl.frames))
	}
	if !strings.Contains(tl.View(), "you: approved plan") {
		t.Error("steer frame should render as 'you: …'")
	}
}

func TestTimeline_AppendSteer_EmptySkipped(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendSteer("")
	if tl.HasContent() {
		t.Error("empty steer should not produce a frame")
	}
}

// --- Live partial (AppendDelta / FlushLive / ClearLive) ---

func TestTimeline_AppendDelta_FlushLive(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendDelta("partial ")
	tl.AppendDelta("text")
	if tl.liveText != "partial text" {
		t.Errorf("liveText = %q, want %q", tl.liveText, "partial text")
	}
	tl.FlushLive()
	if tl.liveText != "" {
		t.Error("FlushLive should clear liveText")
	}
	if len(tl.frames) != 1 {
		t.Fatalf("expected 1 frame after flush, got %d", len(tl.frames))
	}
	if !strings.Contains(tl.View(), "partial text") {
		t.Error("flushed prose should appear in the view")
	}
}

func TestTimeline_ClearLive(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendDelta("delta1")
	tl.ClearLive()
	if tl.liveText != "" {
		t.Errorf("ClearLive should discard liveText, got %q", tl.liveText)
	}
	if tl.HasContent() {
		t.Error("ClearLive should not produce a frame")
	}
}

func TestTimeline_FlushLive_Empty(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.FlushLive()
	if tl.HasContent() {
		t.Error("FlushLive on empty liveText should not produce a frame")
	}
}

// --- HasContent / Clear ---

func TestTimeline_Clear(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendProse("some content")
	tl.AppendDelta("live")
	tl.Clear()
	if tl.HasContent() {
		t.Error("after Clear, HasContent should be false")
	}
	if tl.liveText != "" {
		t.Error("Clear should reset liveText")
	}
	if len(tl.rows) != 0 {
		t.Error("Clear should reset rows")
	}
}

// --- AtBottom / ScrollToBottom / ScrollToTop ---

func TestTimeline_AutoFollow(t *testing.T) {
	tl := newTestTimeline(80, 5)
	for i := range 20 {
		tl.AppendProse(strings.Repeat("x", i+1))
	}
	if !tl.AtBottom() {
		t.Errorf("expected AtBottom after auto-follow, top=%d rows=%d h=%d", tl.top, len(tl.rows), tl.rect.Dy())
	}
}

func TestTimeline_ScrollToTop(t *testing.T) {
	tl := newTestTimeline(80, 5)
	for range 20 {
		tl.AppendProse("line")
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
		tl.AppendProse("line")
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
	tl.active = true
	tl.blinkOn = false
	if !strings.Contains(tl.View(), "⏺") {
		t.Error("View should show live cursor when active")
	}
}

func TestTimeline_View_LiveDeltaText(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.active = true
	tl.AppendDelta("partial output here")
	if !strings.Contains(tl.View(), "partial output here") {
		t.Error("View should show live delta text")
	}
}

func TestTimeline_View_DimToolCollapse(t *testing.T) {
	tl := newTestTimeline(80, 40)
	for i := range constToolFrameMax + 3 {
		tl.AppendToolPending(strings.Repeat("x", i+1))
		tl.ResolveLastTool(false)
	}
	if !strings.Contains(tl.View(), "more tools") {
		t.Error("expected dim collapse summary for excess tool frames")
	}
}

// --- Scroll key handling (via the central keymap) ---

func TestTimeline_ScrollKeys(t *testing.T) {
	tl := newTestTimeline(80, 5)
	for range 30 {
		tl.AppendProse("a long line to force scrolling")
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

func TestTimeline_BlinkMsg_TogglesBlinkOn(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.active = true
	tl.blinkTag = 1
	tl, _ = tl.Update(timelineBlinkMsg{tag: 1})
	if !tl.blinkOn {
		t.Error("blink message should toggle blinkOn to true")
	}
	tl, _ = tl.Update(timelineBlinkMsg{tag: 1})
	if tl.blinkOn {
		t.Error("second blink message should toggle blinkOn back to false")
	}
}

func TestTimeline_BlinkMsg_StaleTagIgnored(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.active = true
	tl.blinkTag = 5
	tl, _ = tl.Update(timelineBlinkMsg{tag: 3}) // stale
	if tl.blinkOn {
		t.Error("stale blink message should not toggle blinkOn")
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
	out, cmd := tl.Update(timelineBlinkMsg{tag: tagBefore})
	if out.blinkOn {
		t.Error("a stale tick after Stop must not toggle the cursor")
	}
	if cmd != nil {
		t.Error("a stale tick after Stop must not reschedule the blink")
	}
}

// --- Selection (visual row/col over rendered cells) ---

func TestTimeline_SelectedText_SingleLine(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendProse("hello world")
	tl.hasSel = true
	tl.anchor = selPos{row: 0, col: 6}
	tl.cursor = selPos{row: 0, col: 11}
	if got := tl.SelectedText(); got != "world" {
		t.Errorf("SelectedText = %q, want %q", got, "world")
	}
}

func TestTimeline_SelectedText_Empty(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendProse("hello")
	tl.hasSel = false
	if tl.SelectedText() != "" {
		t.Error("SelectedText should be empty when no selection")
	}
}

// --- Row rebuild on resize ---

func TestTimeline_Resize_StableRowCount(t *testing.T) {
	tl := newTestTimeline(80, 20)
	for range 10 {
		tl.AppendProse("short")
	}
	rowsBefore := len(tl.rows)
	tl.SetRect(image.Rect(0, 0, 40, 20))
	if len(tl.rows) < rowsBefore {
		t.Errorf("after narrowing, rows decreased: %d -> %d", rowsBefore, len(tl.rows))
	}
}

func TestTimeline_Resize_PreservesContent(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendProse("this line is visible")
	tl.SetRect(image.Rect(0, 0, 40, 20))
	if !strings.Contains(tl.View(), "this line is visible") {
		t.Error("resize should not destroy prose content")
	}
}

// --- Multiple frame types together ---

func TestTimeline_MixedFrames(t *testing.T) {
	tl := newTestTimeline(80, 40)
	tl.AppendPhase("researcher")
	tl.AppendProse("Here is my research.")
	tl.AppendToolPending("read /tmp/data.json")
	tl.ResolveLastTool(false)
	tl.AppendSteer("approved plan")

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
