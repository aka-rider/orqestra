package tui

import (
	"image"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func newTestTimeline(w, h int) Timeline {
	t := NewTimeline(timelineStyles{selectionBg: "#264F78", rule: dividerStyle})
	t.SetRect(image.Rect(0, 0, w, h))
	t.SetFullWidth(w)
	return t
}

// --- AppendProse ---

func TestTimeline_AppendProse_AddsFrame(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendProse("hello world")
	if !tl.HasContent() {
		t.Fatal("expected timeline to have content after AppendProse")
	}
	if len(tl.frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(tl.frames))
	}
	if tl.frames[0].kind != frameKindProse {
		t.Errorf("expected prose frame, got %v", tl.frames[0].kind)
	}
}

func TestTimeline_AppendProse_EmptySkipped(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendProse("")
	// Only truly empty string (after trimming trailing newlines) is skipped.
	if tl.HasContent() {
		t.Error("empty prose should not produce frames")
	}
}

func TestTimeline_AppendProse_StripTrailingNewline(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendProse("line\n")
	if tl.frames[0].rawSource != "line" {
		t.Errorf("trailing newline not stripped, got %q", tl.frames[0].rawSource)
	}
}

// --- AppendPhase ---

func TestTimeline_AppendPhase_AddsRuleFrame(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendPhase("researcher: claude-opus")
	if len(tl.frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(tl.frames))
	}
	if tl.frames[0].kind != frameKindPhase {
		t.Errorf("expected phase frame, got %v", tl.frames[0].kind)
	}
}

// --- AppendToolPending / ResolveLastTool ---

func TestTimeline_ToolPendingAndResolve(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendToolPending("Read /foo/bar.go")

	if len(tl.frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(tl.frames))
	}
	if tl.frames[0].kind != frameKindTool {
		t.Errorf("expected tool frame, got %v", tl.frames[0].kind)
	}
	if tl.frames[0].toolStatus != toolStatusPending {
		t.Errorf("expected pending, got %v", tl.frames[0].toolStatus)
	}

	tl.ResolveLastTool(false)
	if tl.frames[0].toolStatus != toolStatusOK {
		t.Errorf("expected ok after resolve, got %v", tl.frames[0].toolStatus)
	}
}

func TestTimeline_ResolveLastTool_Error(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendToolPending("cat /etc/shadow")
	tl.ResolveLastTool(true)
	if tl.frames[0].toolStatus != toolStatusErr {
		t.Errorf("expected err after resolve, got %v", tl.frames[0].toolStatus)
	}
}

func TestTimeline_ResolveLastTool_MultipleTools(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendToolPending("tool 1")
	tl.ResolveLastTool(false)
	tl.AppendToolPending("tool 2")
	tl.ResolveLastTool(true)

	if tl.frames[0].toolStatus != toolStatusOK {
		t.Errorf("tool 1 should be OK, got %v", tl.frames[0].toolStatus)
	}
	if tl.frames[1].toolStatus != toolStatusErr {
		t.Errorf("tool 2 should be Err, got %v", tl.frames[1].toolStatus)
	}
}

// --- ReconcilePendingTools ---

func TestTimeline_ReconcilePendingTools(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendToolPending("tool 1")
	tl.AppendToolPending("tool 2")
	tl.ReconcilePendingTools()

	for i, f := range tl.frames {
		if f.kind == frameKindTool && f.toolStatus != toolStatusUnknown {
			t.Errorf("frame %d: expected unknown after reconcile, got %v", i, f.toolStatus)
		}
	}
}

// --- AppendSteer ---

func TestTimeline_AppendSteer(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendSteer("approved plan")
	if len(tl.frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(tl.frames))
	}
	if tl.frames[0].kind != frameKindSteer {
		t.Errorf("expected steer frame, got %v", tl.frames[0].kind)
	}
	if tl.frames[0].rawSource != "approved plan" {
		t.Errorf("wrong rawSource: %q", tl.frames[0].rawSource)
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
	if tl.frames[0].rawSource != "partial text" {
		t.Errorf("rawSource after flush = %q", tl.frames[0].rawSource)
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
	tl.FlushLive() // no-op on empty live
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
	// Append more lines than the viewport.
	for i := range 20 {
		tl.AppendProse(strings.Repeat("x", i+1))
	}
	// With follow=true (default), top should be at or near the last row.
	if !tl.AtBottom() {
		t.Errorf("expected AtBottom after auto-follow appends, top=%d rows=%d h=%d",
			tl.top, len(tl.rows), tl.rect.Dy())
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
		t.Errorf("ScrollToBottom: not at bottom (top=%d rows=%d h=%d)",
			tl.top, len(tl.rows), tl.rect.Dy())
	}
}

// --- View output ---

func TestTimeline_View_ContainsAppendedText(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendProse("hello from timeline")
	view := tl.View()
	if !strings.Contains(view, "hello from timeline") {
		t.Error("View should contain appended prose text")
	}
}

func TestTimeline_View_ShowsLiveCursor(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.active = true
	tl.blinkOn = false
	view := tl.View()
	if !strings.Contains(view, "⏺") {
		t.Error("View should show live cursor when active")
	}
}

func TestTimeline_View_LiveDeltaText(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.active = true
	tl.AppendDelta("partial output here")
	view := tl.View()
	if !strings.Contains(view, "partial output here") {
		t.Error("View should show live delta text")
	}
}

func TestTimeline_View_ToolStatus(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendToolPending("read /tmp/foo")
	viewPending := tl.View()
	if !strings.Contains(viewPending, "◌") {
		t.Error("pending tool should render ◌ icon")
	}
	tl.ResolveLastTool(false)
	viewOK := tl.View()
	if !strings.Contains(viewOK, "✓") {
		t.Error("resolved tool should render ✓ icon")
	}
}

func TestTimeline_View_DimToolCollapse(t *testing.T) {
	tl := newTestTimeline(80, 40)
	// Append more tools than constToolBrightCount.
	for i := range constToolBrightCount + 3 {
		tl.AppendToolPending(strings.Repeat("x", i+1))
		tl.ResolveLastTool(false)
	}
	view := tl.View()
	// The summary line should appear.
	if !strings.Contains(view, "more tools") {
		t.Error("expected dim collapse summary for excess tool frames")
	}
}

// --- Scroll key handling ---

func TestTimeline_ScrollKeys(t *testing.T) {
	tl := newTestTimeline(80, 5)
	for range 30 {
		tl.AppendProse("a long line to force scrolling")
	}
	tl.ScrollToTop()
	topBefore := tl.top

	// PgDn should move down.
	tl, _ = tl.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if tl.top <= topBefore {
		t.Errorf("pgdn: top should increase from %d, got %d", topBefore, tl.top)
	}

	// PgUp should move back up.
	topAfter := tl.top
	tl, _ = tl.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if tl.top >= topAfter {
		t.Errorf("pgup: top should decrease from %d, got %d", topAfter, tl.top)
	}
}

// --- Blink msg ---

func TestTimeline_BlinkMsg_TogglesBlinkOn(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.active = true
	tl.blinkTag = 1
	tl.blinkOn = false
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
	tl.blinkOn = false
	tl, _ = tl.Update(timelineBlinkMsg{tag: 3}) // stale
	if tl.blinkOn {
		t.Error("stale blink message should not toggle blinkOn")
	}
}

// --- SelectedText ---

func TestTimeline_SelectedText_SingleLine(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendProse("hello world")
	tl.hasSel = true
	tl.anchor = timelinePos{line: 0, col: 6}
	tl.cursor = timelinePos{line: 0, col: 11}
	text := tl.SelectedText()
	if text != "world" {
		t.Errorf("SelectedText = %q, want %q", text, "world")
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

	// Resize to a narrower width — word wrap may increase row count.
	tl.SetRect(image.Rect(0, 0, 40, 20))
	// Row count must be >= original (never fewer due to narrowing).
	if len(tl.rows) < rowsBefore {
		t.Errorf("after resize, rows decreased: %d -> %d", rowsBefore, len(tl.rows))
	}
}

func TestTimeline_Resize_PreservesContent(t *testing.T) {
	tl := newTestTimeline(80, 20)
	tl.AppendProse("this line is visible")
	tl.SetRect(image.Rect(0, 0, 40, 20))
	view := tl.View()
	if !strings.Contains(view, "this line is visible") {
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
	if tl.frames[0].kind != frameKindPhase {
		t.Errorf("frame 0: expected phase, got %v", tl.frames[0].kind)
	}
	if tl.frames[1].kind != frameKindProse {
		t.Errorf("frame 1: expected prose, got %v", tl.frames[1].kind)
	}
	if tl.frames[2].kind != frameKindTool {
		t.Errorf("frame 2: expected tool, got %v", tl.frames[2].kind)
	}
	if tl.frames[3].kind != frameKindSteer {
		t.Errorf("frame 3: expected steer, got %v", tl.frames[3].kind)
	}
}
