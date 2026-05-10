package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// layoutTestModel creates a model at the given dimensions, suitable for layout tests.
func layoutTestModel(w, h int, state AppState) Model {
	m := testModel()
	m.width = w
	m.height = h
	m.state = state
	m.recalculateLayout()
	return m
}

func TestLayout_NoPanicAtBoundarySizes(t *testing.T) {
	sizes := [][2]int{
		{minWidth, minHeight},
		{80, 24},
		{120, 40},
		{200, 60},
		{59, 9},  // below minimum
		{60, 10}, // exact minimum
		{1, 1},   // pathological
	}
	states := []AppState{StatePrompt, StatePipeline}

	for _, sz := range sizes {
		for _, st := range states {
			name := "prompt"
			if st == StatePipeline {
				name = "pipeline"
			}
			t.Run(name+"-"+itoa(sz[0])+"x"+itoa(sz[1]), func(t *testing.T) {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("panic at %dx%d: %v", sz[0], sz[1], r)
					}
				}()
				m := layoutTestModel(sz[0], sz[1], st)
				_ = m.View()
			})
		}
	}
}

func TestLayout_HeightInvariant(t *testing.T) {
	sizes := [][2]int{
		{80, 24},
		{120, 40},
		{200, 60},
	}
	states := []struct {
		name  string
		state AppState
	}{
		{"prompt", StatePrompt},
		{"pipeline", StatePipeline},
	}

	for _, sz := range sizes {
		for _, st := range states {
			t.Run(st.name+"-"+itoa(sz[0])+"x"+itoa(sz[1]), func(t *testing.T) {
				m := layoutTestModel(sz[0], sz[1], st.state)
				view := m.View().Content
				got := lipgloss.Height(view)
				if got != sz[1] {
					t.Errorf("lipgloss.Height(View()) = %d, want %d", got, sz[1])
				}
			})
		}
	}
}

func TestLayout_HeightInvariant_SmallTerminal(t *testing.T) {
	// At small sizes, the prompt screen may skip the split view;
	// verify it doesn't panic and doesn't exceed the requested height.
	m := layoutTestModel(60, 10, StatePrompt)
	view := m.View().Content
	got := lipgloss.Height(view)
	if got > 10 {
		t.Errorf("prompt at 60x10: lipgloss.Height = %d, want <= 10", got)
	}

	// Pipeline at 60x10 should match exactly
	m2 := layoutTestModel(60, 10, StatePipeline)
	view2 := m2.View().Content
	got2 := lipgloss.Height(view2)
	if got2 != 10 {
		t.Errorf("pipeline at 60x10: lipgloss.Height = %d, want 10", got2)
	}
}

func TestLayout_RecalculateConstants(t *testing.T) {
	m := layoutTestModel(120, 40, StatePipeline)

	contentWidth := int(float64(120) * splitRatio)
	sidebarWidth := 120 - contentWidth - 1
	contentHeight := 40 - constHeaderHeight - constPipelineInputHeight - constFooterHeight

	if m.pipelineScreen.contentVP.Width() != contentWidth {
		t.Errorf("contentVP.Width = %d, want %d", m.pipelineScreen.contentVP.Width(), contentWidth)
	}
	if m.pipelineScreen.contentVP.Height() != contentHeight {
		t.Errorf("contentVP.Height = %d, want %d", m.pipelineScreen.contentVP.Height(), contentHeight)
	}
	if m.pipelineScreen.sidebarVP.Width() != sidebarWidth {
		t.Errorf("sidebarVP.Width = %d, want %d", m.pipelineScreen.sidebarVP.Width(), sidebarWidth)
	}
	if m.pipelineScreen.dashboardVP.Width() != 120 {
		t.Errorf("dashboardVP.Width = %d, want 120", m.pipelineScreen.dashboardVP.Width())
	}
}

func TestLayout_RecalculatePromptMode(t *testing.T) {
	m := layoutTestModel(120, 40, StatePrompt)

	contentHeight := 40 - constHeaderHeight - constPromptInputHeight - constFooterHeight

	if m.pipelineScreen.contentVP.Height() != contentHeight {
		t.Errorf("contentVP.Height = %d, want %d (prompt mode)", m.pipelineScreen.contentVP.Height(), contentHeight)
	}
}

func TestLayout_BelowMinimumNoOp(t *testing.T) {
	m := testModel()
	m.width = 30
	m.height = 5
	m.recalculateLayout()

	// Viewports should remain at zero (not set to negative)
	if m.pipelineScreen.contentVP.Width() < 0 || m.pipelineScreen.contentVP.Height() < 0 {
		t.Error("viewport dimensions should not be negative below minimum size")
	}
}

func TestLayout_JoinSplitView(t *testing.T) {
	left := "left\ncontent"
	right := "right\nside"
	result := joinSplitView(left, right, 20, 10, 5)

	lines := strings.Split(result, "\n")
	if len(lines) < 5 {
		t.Errorf("joinSplitView produced %d lines, want at least 5", len(lines))
	}
}

func TestLayout_SplitRatioDerivation(t *testing.T) {
	widths := []int{60, 80, 120, 200}
	for _, w := range widths {
		contentW := int(float64(w) * splitRatio)
		sidebarW := w - contentW - 1
		total := contentW + 1 + sidebarW // content + separator + sidebar
		if total != w {
			t.Errorf("width=%d: content(%d) + sep(1) + sidebar(%d) = %d, want %d",
				w, contentW, sidebarW, total, w)
		}
	}
}

func TestLayout_BoundsNonOverlapping(t *testing.T) {
	m := layoutTestModel(120, 40, StatePipeline)

	// Content and sidebar should not overlap
	if m.pipelineScreen.bounds.content.Overlaps(m.pipelineScreen.bounds.sidebar) {
		t.Error("content and sidebar bounds overlap")
	}
	// Textarea should be below content
	if m.pipelineScreen.bounds.textarea.Min.Y <= m.pipelineScreen.bounds.content.Min.Y {
		t.Error("textarea should be below content zone")
	}
}

// itoa is a simple int-to-string for test names without importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}
