package tui

import (
	"strings"
	"testing"
	"time"
)

func TestRenderFrame_Init(t *testing.T) {
	f := Frame{
		Kind:       AgentFrame,
		State:      FrameInit,
		AgentID:    "Researcher",
		AgentModel: "claude-sonnet-4-20250514",
	}
	got := renderFrame(&f, 60, 0, false)
	if got == "" {
		t.Fatal("renderFrame returned empty string for init frame")
	}
	assertContains(t, got, "Researcher")
	assertContains(t, got, "claude-sonnet-4-20250514")
	assertContains(t, got, "…") // init indicator
}

func TestRenderFrame_InProgress(t *testing.T) {
	f := Frame{
		Kind:       AgentFrame,
		State:      FrameInProgress,
		AgentID:    "Architect",
		AgentModel: "claude-sonnet-4-20250514",
		Parts: []ContentPart{
			{IsText: true, Text: "Analyzing the codebase...\n"},
		},
		Partial: "streaming text continues",
	}
	got := renderFrame(&f, 60, 0, false)
	assertContains(t, got, "Architect")
	assertContains(t, got, "·∘○∘·") // shimmer frame 0
	assertContains(t, got, "Analyzing the codebase...")
	assertContains(t, got, "▎streaming text continues")
}

func TestRenderFrame_InProgress_ShimmerCycles(t *testing.T) {
	f := Frame{
		Kind:    AgentFrame,
		State:   FrameInProgress,
		AgentID: "Worker",
	}
	got0 := renderFrame(&f, 60, 0, false)
	got1 := renderFrame(&f, 60, 1, false)
	assertContains(t, got0, "·∘○∘·")
	assertContains(t, got1, "∘○∘·∘")
}

func TestRenderFrame_Finished(t *testing.T) {
	f := Frame{
		Kind:         AgentFrame,
		State:        FrameFinished,
		AgentID:      "Researcher",
		AgentModel:   "claude-sonnet-4-20250514",
		Elapsed:      12 * time.Second,
		InputTokens:  5000,
		OutputTokens: 1200,
		Parts: []ContentPart{
			{IsText: true, Text: "The codebase uses pattern X.\n"},
		},
	}
	got := renderFrame(&f, 60, 0, false)
	assertContains(t, got, "Researcher")
	assertContains(t, got, "✓")
	assertContains(t, got, "12s")
	assertContains(t, got, "The codebase uses pattern X.")
}

func TestRenderFrame_WithToolBlocks(t *testing.T) {
	f := Frame{
		Kind:    AgentFrame,
		State:   FrameInProgress,
		AgentID: "Researcher",
		Parts: []ContentPart{
			{IsText: true, Text: "Let me explore...\n"},
			{IsText: false, Tool: ToolBlock{Name: "Read", Detail: "internal/foo/bar.go"}},
			{IsText: true, Text: "Found the relevant code.\n"},
			{IsText: false, Tool: ToolBlock{Name: "Bash", Detail: "go test ./..."}},
		},
	}
	got := renderFrame(&f, 60, 0, false)
	assertContains(t, got, "Let me explore...")
	assertContains(t, got, "Read")
	assertContains(t, got, "internal/foo/bar.go")
	assertContains(t, got, "Found the relevant code.")
	assertContains(t, got, "Bash")
	assertContains(t, got, "go test ./...")
}

func TestRenderFrame_PlanFrame(t *testing.T) {
	f := Frame{
		Kind:  PlanFrame,
		State: FrameFinished,
		Parts: []ContentPart{
			{IsText: true, Text: "## Implementation Plan\n1. Create foo.go\n2. Modify bar.go\n"},
		},
	}
	got := renderFrame(&f, 60, 0, false)
	assertContains(t, got, "Plan")
	assertContains(t, got, "## Implementation Plan")
}

func TestRenderFrame_CompletionFrame(t *testing.T) {
	f := Frame{
		Kind:    CompletionFrame,
		State:   FrameFinished,
		Elapsed: 3*time.Minute + 22*time.Second,
		Parts: []ContentPart{
			{IsText: true, Text: "Pipeline complete. All steps passed.\n"},
		},
	}
	got := renderFrame(&f, 60, 0, false)
	assertContains(t, got, "Complete")
	assertContains(t, got, "3m22s")
}

func TestRenderToolBlock(t *testing.T) {
	tb := ToolBlock{Name: "Read", Detail: "internal/foo/bar.go"}
	got := renderToolBlock(tb, 50)
	assertContains(t, got, "✑") // Read icon
	assertContains(t, got, "Read")
	assertContains(t, got, "internal/foo/bar.go")
}

func TestRenderToolBlock_Bash(t *testing.T) {
	tb := ToolBlock{Name: "Bash", Detail: "go test ./..."}
	got := renderToolBlock(tb, 50)
	assertContains(t, got, "❯") // Bash icon
	assertContains(t, got, "Bash")
}

func TestFrameList_DirtyFlag(t *testing.T) {
	fl := NewFrameList(80)

	// Initially dirty
	if !fl.IsDirty() {
		t.Fatal("new FrameList should be dirty")
	}

	// Render clears dirty
	_ = fl.Render()
	if fl.IsDirty() {
		t.Fatal("Render should clear dirty flag")
	}

	// AppendFrame marks dirty
	fl.AppendFrame(Frame{Kind: AgentFrame, State: FrameInProgress, AgentID: "Test"})
	if !fl.IsDirty() {
		t.Fatal("AppendFrame should mark dirty")
	}

	_ = fl.Render()
	if fl.IsDirty() {
		t.Fatal("Render should clear dirty flag")
	}

	// SetWidth with same width does NOT mark dirty
	fl.SetWidth(80)
	if fl.IsDirty() {
		t.Fatal("SetWidth with same value should not mark dirty")
	}

	// SetWidth with different width marks dirty
	fl.SetWidth(100)
	if !fl.IsDirty() {
		t.Fatal("SetWidth with different value should mark dirty")
	}
}

func TestFrameList_RenderCaching(t *testing.T) {
	fl := NewFrameList(60)
	fl.AppendFrame(Frame{
		Kind:    AgentFrame,
		State:   FrameInProgress,
		AgentID: "Worker",
		Parts:   []ContentPart{{IsText: true, Text: "hello\n"}},
	})

	r1 := fl.Render()
	r2 := fl.Render()
	if r1 != r2 {
		t.Fatal("cached Render should return identical string")
	}
}

func TestFrameList_FrameTopLine(t *testing.T) {
	fl := NewFrameList(60)
	fl.AppendFrame(Frame{
		Kind:    AgentFrame,
		State:   FrameFinished,
		AgentID: "First",
		Elapsed: 5 * time.Second,
		Parts:   []ContentPart{{IsText: true, Text: "line1\nline2\n"}},
	})
	fl.AppendFrame(Frame{
		Kind:    AgentFrame,
		State:   FrameInProgress,
		AgentID: "Second",
		Parts:   []ContentPart{{IsText: true, Text: "content\n"}},
	})

	top0 := fl.FrameTopLine(0)
	top1 := fl.FrameTopLine(1)

	if top0 != 0 {
		t.Fatalf("FrameTopLine(0) = %d, want 0", top0)
	}
	if top1 <= 0 {
		t.Fatalf("FrameTopLine(1) should be > 0, got %d", top1)
	}
}

func TestFrameList_SetAnimFrame(t *testing.T) {
	fl := NewFrameList(60)

	// No InProgress frame — SetAnimFrame should NOT mark dirty
	fl.AppendFrame(Frame{Kind: AgentFrame, State: FrameFinished, AgentID: "Done"})
	_ = fl.Render()
	fl.SetAnimFrame(1)
	if fl.IsDirty() {
		t.Fatal("SetAnimFrame should not mark dirty without InProgress frame")
	}

	// With InProgress frame — SetAnimFrame should mark dirty
	fl.AppendFrame(Frame{Kind: AgentFrame, State: FrameInProgress, AgentID: "Active"})
	_ = fl.Render()
	fl.SetAnimFrame(2)
	if !fl.IsDirty() {
		t.Fatal("SetAnimFrame should mark dirty with InProgress frame")
	}
}

func TestFrame_TextCoalescing(t *testing.T) {
	f := &Frame{Kind: AgentFrame, State: FrameInProgress}

	f.AppendText("line1")
	f.AppendText("line2")

	if len(f.Parts) != 1 {
		t.Fatalf("expected 1 part after consecutive text, got %d", len(f.Parts))
	}
	if f.Parts[0].Text != "line1\nline2\n" {
		t.Fatalf("unexpected coalesced text: %q", f.Parts[0].Text)
	}

	// Tool interrupts coalescing
	f.AppendTool(ToolBlock{Name: "Read", Detail: "foo.go"})
	f.AppendText("line3")

	if len(f.Parts) != 3 {
		t.Fatalf("expected 3 parts after tool interruption, got %d", len(f.Parts))
	}
	if f.Parts[2].Text != "line3\n" {
		t.Fatalf("unexpected text after tool: %q", f.Parts[2].Text)
	}
}

func TestFrame_AppendTool_FlushesPartial(t *testing.T) {
	f := &Frame{Kind: AgentFrame, State: FrameInProgress}
	f.Partial = "incomplete line"

	f.AppendTool(ToolBlock{Name: "Write", Detail: "out.go"})

	if f.Partial != "" {
		t.Fatal("AppendTool should flush Partial")
	}
	if len(f.Parts) != 2 {
		t.Fatalf("expected 2 parts (flushed text + tool), got %d", len(f.Parts))
	}
	if !f.Parts[0].IsText || f.Parts[0].Text != "incomplete line\n" {
		t.Fatalf("flushed partial should be first part, got %q", f.Parts[0].Text)
	}
}

func TestFormatElapsed(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Millisecond, "<1s"},
		{1 * time.Second, "1s"},
		{12 * time.Second, "12s"},
		{60 * time.Second, "1m"},
		{90 * time.Second, "1m30s"},
		{3*time.Minute + 22*time.Second, "3m22s"},
	}
	for _, tt := range tests {
		got := formatElapsed(tt.d)
		if got != tt.want {
			t.Errorf("formatElapsed(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestFrameList_ScrollStability(t *testing.T) {
	// When frame list is not dirty, Render should return the same cached value
	// (simulates scroll stability across ticks with no new data).
	fl := NewFrameList(60)
	fl.AppendFrame(Frame{
		Kind:    AgentFrame,
		State:   FrameInProgress,
		AgentID: "Worker",
		Parts:   []ContentPart{{IsText: true, Text: "line1\nline2\nline3\n"}},
	})

	r1 := fl.Render()
	// Simulate multiple ticks with same animFrame
	r2 := fl.Render()
	r3 := fl.Render()

	if r1 != r2 || r2 != r3 {
		t.Fatal("Render should return identical cached string across ticks when not dirty")
	}

	// Verify IsDirty is false after render
	if fl.IsDirty() {
		t.Fatal("IsDirty should be false after Render")
	}
}

func TestFrameList_ResizeMarksDirty(t *testing.T) {
	fl := NewFrameList(60)
	fl.AppendFrame(Frame{Kind: AgentFrame, State: FrameFinished, AgentID: "A"})
	_ = fl.Render()

	if fl.IsDirty() {
		t.Fatal("should not be dirty after render")
	}

	fl.SetWidth(80)
	if !fl.IsDirty() {
		t.Fatal("SetWidth with new value should mark dirty")
	}

	r1 := fl.Render()
	fl.SetWidth(80)
	r2 := fl.Render()

	if r1 != r2 {
		t.Fatal("same width should produce same render")
	}
}

func TestFrameList_AutoFollowBehavior(t *testing.T) {
	// Frame list stays dirty when new content arrives, allowing viewport to update.
	fl := NewFrameList(60)
	fl.AppendFrame(Frame{Kind: AgentFrame, State: FrameInProgress, AgentID: "W"})
	_ = fl.Render() // clear dirty

	// New text arrives
	fl.UpdateActive(func(f *Frame) { f.AppendText("new line") })
	if !fl.IsDirty() {
		t.Fatal("UpdateActive should mark dirty so viewport refreshes")
	}
}

func TestFrameList_PlanGateHintRendered(t *testing.T) {
	fl := NewFrameList(60)
	fl.AppendFrame(Frame{
		Kind:  PlanFrame,
		State: FrameInProgress,
		Parts: []ContentPart{{IsText: true, Text: "## Plan\n1. Do X\n"}},
	})

	rendered := fl.Render()
	assertContains(t, rendered, "[^A] accept")
	assertContains(t, rendered, "[Enter] comment")
}

func TestFrameList_PlanGateHintRemovedAfterFinish(t *testing.T) {
	fl := NewFrameList(60)
	fl.AppendFrame(Frame{
		Kind:  PlanFrame,
		State: FrameInProgress,
		Parts: []ContentPart{{IsText: true, Text: "Plan content\n"}},
	})
	fl.FinishActive(0, 0, 0)

	rendered := fl.Render()
	if containsStr(rendered, "[^A] accept") {
		t.Fatal("hint should not render after PlanFrame is finished")
	}
}

func TestRenderFrame_Focused(t *testing.T) {
	f := Frame{Kind: AgentFrame, State: FrameFinished, AgentID: "Worker", Elapsed: 5 * time.Second,
		Parts: []ContentPart{{IsText: true, Text: "done\n"}}}
	got := renderFrame(&f, 60, 0, true)
	if got == "" {
		t.Fatal("focused frame should not be empty")
	}
	assertContains(t, got, "Worker")
	assertContains(t, got, "✓")
}

func TestRenderFrame_Collapsed(t *testing.T) {
	f := Frame{Kind: AgentFrame, State: FrameFinished, AgentID: "Researcher", Elapsed: 3 * time.Second,
		Collapsed: true,
		Parts:     []ContentPart{{IsText: true, Text: "The codebase uses pattern X.\n"}}}
	got := renderFrame(&f, 60, 0, false)
	assertContains(t, got, "Researcher")
	assertContains(t, got, "The codebase uses pattern X")
	if strings.Count(got, "\n") > 5 {
		t.Errorf("collapsed frame should be compact; got %d lines", strings.Count(got, "\n"))
	}
}

func TestFrameList_NewFrameList_FocusedIsNegOne(t *testing.T) {
	fl := NewFrameList(80)
	if fl.FocusedIndex() != -1 {
		t.Fatalf("expected -1, got %d", fl.FocusedIndex())
	}
}

func TestFrameList_FocusNavigation(t *testing.T) {
	fl := NewFrameList(60)
	fl.AppendFrame(Frame{Kind: AgentFrame, State: FrameFinished, AgentID: "A"})
	fl.AppendFrame(Frame{Kind: AgentFrame, State: FrameFinished, AgentID: "B"})

	fl.FocusNext() // from -1 → 0
	if fl.FocusedIndex() != 0 {
		t.Fatalf("expected 0, got %d", fl.FocusedIndex())
	}

	fl.FocusPrev() // from 0 → 1 (wraps)
	if fl.FocusedIndex() != 1 {
		t.Fatalf("expected 1 (wrap), got %d", fl.FocusedIndex())
	}

	fl.ClearFocus()
	fl.FocusPrev() // from -1 → 1 (last)
	if fl.FocusedIndex() != 1 {
		t.Fatalf("expected 1 (last), got %d", fl.FocusedIndex())
	}
}

func TestFrameList_ToggleFocused_InProgress_NoOp(t *testing.T) {
	fl := NewFrameList(60)
	fl.AppendFrame(Frame{Kind: AgentFrame, State: FrameInProgress, AgentID: "W"})
	fl.FocusFrame(0)
	fl.ToggleFocused()
	if fl.frames[0].Collapsed {
		t.Fatal("ToggleFocused should not collapse InProgress frame")
	}
}

func assertContains(t *testing.T, got, want string) {
	t.Helper()
	if !containsStr(got, want) {
		t.Errorf("expected output to contain %q, got:\n%s", want, got)
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
