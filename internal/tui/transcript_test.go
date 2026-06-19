package tui

import (
	"image"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func testTranscriptStyles() transcriptStyles {
	return transcriptStyles{
		selectionBg: "238",
		rule:        dividerStyle,
	}
}

func newTestTranscript(w, h int) Transcript {
	t := NewTranscript(testTranscriptStyles())
	t.SetRect(image.Rect(0, 0, w, h))
	return t
}

func textLine(text string) transcriptLine { return newTextLine(text) }

func styledLine(spans ...transcriptSpan) transcriptLine {
	return transcriptLine{kind: transcriptLineText, spans: spans}
}

func ruleLine(label string) transcriptLine { return newRuleLine(label) }

// --- Render purity ---

func TestTranscript_RenderPurity(t *testing.T) {
	tr := newTestTranscript(40, 5)
	tr.Append(textLine("hello"), textLine("world"))

	v1 := tr.View()
	v2 := tr.View()
	v3 := tr.View()
	if v1 != v2 || v2 != v3 {
		t.Error("View() is not pure: repeated calls produce different output")
	}
}

// --- Scroll follow ---

func TestTranscript_AutoFollowBottom(t *testing.T) {
	tr := newTestTranscript(40, 5)
	for i := range 20 {
		tr.Append(textLine(strings.Repeat("x", i+1)))
	}
	if !tr.AtBottom() {
		t.Error("expected transcript to be at bottom after auto-follow appends")
	}
}

func TestTranscript_ScrollUpDisablesFollow(t *testing.T) {
	tr := newTestTranscript(40, 5)
	for range 20 {
		tr.Append(textLine("line"))
	}
	tr, _ = tr.Update(wheelUpMsg())
	if tr.AtBottom() {
		t.Error("expected not at bottom after scrolling up")
	}
	prevTop := tr.top
	tr.Append(textLine("new line"))
	if tr.top != prevTop {
		t.Error("scrolled-up transcript should not follow new appends")
	}
}

func TestTranscript_WheelDownRestoresFollow(t *testing.T) {
	tr := newTestTranscript(40, 5)
	for range 20 {
		tr.Append(textLine("line"))
	}
	tr, _ = tr.Update(wheelUpMsg())
	// Scroll back to bottom
	for range 10 {
		tr, _ = tr.Update(wheelDownMsg())
	}
	if !tr.AtBottom() {
		t.Error("expected to be at bottom again after scrolling down to end")
	}
}

func TestTranscript_ResizePreservesPin(t *testing.T) {
	tr := newTestTranscript(40, 5)
	for range 20 {
		tr.Append(textLine("line"))
	}
	// Should be at bottom (follow)
	if !tr.AtBottom() {
		t.Fatal("precondition: expected at bottom")
	}
	// Resize wider
	tr.SetRect(image.Rect(0, 0, 80, 5))
	if !tr.AtBottom() {
		t.Error("expected to stay at bottom after resize when pinned")
	}
}

func TestTranscript_ScrollUpResizeStaysScrolled(t *testing.T) {
	tr := newTestTranscript(40, 10)
	for range 30 {
		tr.Append(textLine("line"))
	}
	tr, _ = tr.Update(wheelUpMsg()) // scroll up
	prevTop := tr.top
	tr.SetRect(image.Rect(0, 0, 40, 10)) // same size resize
	if tr.top != prevTop {
		t.Errorf("top changed after same-size resize: got %d, want %d", tr.top, prevTop)
	}
}

// --- Selection ---

func TestTranscript_Selection_AcrossLines(t *testing.T) {
	tr := newTestTranscript(40, 10)
	tr.Append(textLine("hello"))
	tr.Append(textLine("world"))

	tr.anchor = transcriptPos{line: 0, col: 1}
	tr.cursor = transcriptPos{line: 1, col: 3}
	tr.hasSel = true

	got := tr.SelectedText()
	if !strings.Contains(got, "ello") {
		t.Errorf("expected 'ello' in selection, got %q", got)
	}
	if !strings.Contains(got, "wor") {
		t.Errorf("expected 'wor' in selection, got %q", got)
	}
}

func TestTranscript_Selection_SkipsRules(t *testing.T) {
	tr := newTestTranscript(40, 10)
	tr.Append(textLine("before"))
	tr.Append(ruleLine("phase"))
	tr.Append(textLine("after"))

	tr.anchor = transcriptPos{line: 0, col: 0}
	tr.cursor = transcriptPos{line: 2, col: 5}
	tr.hasSel = true

	got := tr.SelectedText()
	if strings.Contains(got, "────") {
		t.Errorf("rule separator leaked into selection: %q", got)
	}
	if !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Errorf("expected before+after in selection, got %q", got)
	}
	// Separator should be a blank line (skip → single \n between lines)
	if strings.Count(got, "\n") < 1 {
		t.Errorf("expected at least one newline, got %q", got)
	}
}

func TestTranscript_Selection_SurvivesRewrap(t *testing.T) {
	tr := newTestTranscript(40, 10)
	tr.Append(textLine("hello world"))

	tr.anchor = transcriptPos{line: 0, col: 0}
	tr.cursor = transcriptPos{line: 0, col: 5}
	tr.hasSel = true

	text40 := tr.SelectedText()

	tr.SetRect(image.Rect(0, 0, 80, 10))
	text80 := tr.SelectedText()

	tr.SetRect(image.Rect(0, 0, 20, 10))
	text20 := tr.SelectedText()

	if text40 != text80 || text80 != text20 {
		t.Errorf("selected text changed on rewrap: %q / %q / %q", text40, text80, text20)
	}
}

// --- Soft-wrap correctness ---

func TestTranscript_SoftWrap_LongLine(t *testing.T) {
	tr := newTestTranscript(10, 20)
	long := strings.Repeat("abcde", 4) // 20 chars, wraps at 10
	tr.Append(textLine(long))
	// Should have 2+ display rows for one logical line
	if len(tr.rows) < 2 {
		t.Errorf("expected ≥2 display rows for 20-char line at width 10, got %d", len(tr.rows))
	}
	// All rows should point to line 0
	for i, row := range tr.rows {
		if row.lineIdx != 0 {
			t.Errorf("row %d has lineIdx %d, want 0", i, row.lineIdx)
		}
	}
}

func TestTranscript_SoftWrap_WordBoundary(t *testing.T) {
	// "hello world" at width 8: "hello" fits (5), space (6), "world" would hit 11 > 8.
	// Word-boundary break at the space → row0="hello", row1="world".
	tr := newTestTranscript(8, 10)
	tr.Append(textLine("hello world"))
	if len(tr.rows) < 2 {
		t.Fatalf("expected ≥2 rows, got %d", len(tr.rows))
	}
	// First row should contain "hello", second "world".
	row0text := func(r transcriptRow) string {
		var b strings.Builder
		for _, c := range r.cells {
			b.WriteRune(c.r)
		}
		return b.String()
	}
	if got := row0text(tr.rows[0]); got != "hello" {
		t.Errorf("row0 cells = %q, want %q", got, "hello")
	}
	if got := row0text(tr.rows[1]); got != "world" {
		t.Errorf("row1 cells = %q, want %q", got, "world")
	}
}

// --- Rule rendering ---

func TestTranscript_RuleRendering(t *testing.T) {
	tr := newTestTranscript(40, 5)
	tr.Append(ruleLine("phase"))
	v := tr.View()
	if !strings.Contains(v, "phase") {
		t.Errorf("expected 'phase' in rule rendering, got %q", v)
	}
	if !strings.Contains(v, "────") {
		t.Errorf("expected ────... in rule rendering, got %q", v)
	}
}

// --- Autoscroll stale-tick guard ---

func TestTranscript_AutoscrollStaleTickIsNoop(t *testing.T) {
	tr := newTestTranscript(40, 5)
	for range 20 {
		tr.Append(textLine("line"))
	}
	tr, _ = tr.Update(wheelUpMsg())
	savedTop := tr.top
	savedSeq := tr.dragSeq

	// A stale autoscroll tick (seq != dragSeq) must be a no-op.
	tr, cmd := tr.handleAutoscrollTick(transcriptAutoscrollMsg{seq: savedSeq + 99})
	if tr.top != savedTop {
		t.Errorf("stale autoscroll tick changed top: got %d, want %d", tr.top, savedTop)
	}
	if cmd != nil {
		t.Error("stale autoscroll tick should not return a cmd")
	}
}

// --- scrollFollow port ---

func TestScrollFollow_CursorBelowViewport(t *testing.T) {
	// cursor at row 12, viewport shows rows [0, 9], margin=1
	got := scrollFollow(12, 0, 10, 20, 1, 0)
	if got+10-1 < 12 {
		t.Errorf("cursor %d not visible in [%d, %d)", 12, got, got+10)
	}
}

func TestScrollFollow_CursorAboveViewport(t *testing.T) {
	got := scrollFollow(2, 8, 10, 20, 1, 0)
	if got > 2 {
		t.Errorf("expected offset ≤ cursor 2, got %d", got)
	}
}

func TestScrollFollow_ClampedToContent(t *testing.T) {
	got := scrollFollow(5, 0, 10, 8, 1, 0)
	if got != 0 {
		t.Errorf("expected 0 when total < size, got %d", got)
	}
}

func TestScrollFollow_ZeroSizeReturnsZero(t *testing.T) {
	got := scrollFollow(5, 3, 0, 20, 1, 0)
	if got != 0 {
		t.Errorf("expected 0 for zero-size viewport, got %d", got)
	}
}

// --- helpers ---

func wheelUpMsg() tea.MouseWheelMsg    { return tea.MouseWheelMsg{Button: tea.MouseWheelUp} }
func wheelDownMsg() tea.MouseWheelMsg  { return tea.MouseWheelMsg{Button: tea.MouseWheelDown} }
