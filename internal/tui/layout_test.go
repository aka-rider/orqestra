package tui

import (
	"fmt"
	"testing"

	"charm.land/bubbles/v2/textarea"
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

	// New geometry: full-width content, constSidebarHeight=1 (status bar)
	contentHeight := 40 - constPipelineInputHeight - constFooterHeight - constSidebarHeight

	if m.pipelineScreen.contentVP.Width() != 120 {
		t.Errorf("contentVP.Width = %d, want 120", m.pipelineScreen.contentVP.Width())
	}
	if m.pipelineScreen.contentVP.Height() != contentHeight {
		t.Errorf("contentVP.Height = %d, want %d", m.pipelineScreen.contentVP.Height(), contentHeight)
	}
	if m.pipelineScreen.dashboard.width != 120 {
		t.Errorf("dashboard.width = %d, want 120", m.pipelineScreen.dashboard.width)
	}
}

func TestLayout_RecalculatePromptMode(t *testing.T) {
	m := layoutTestModel(120, 40, StatePrompt)

	contentHeight := 40 - constPromptInputHeight - constFooterHeight - constSidebarHeight

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

func TestLayout_StatusBarGeometry(t *testing.T) {
	// Status bar is 1 line — just verify the layout math is consistent
	widths := []int{60, 80, 120, 200}
	for _, w := range widths {
		m := layoutTestModel(w, 40, StatePipeline)
		expectedContent := 40 - constPipelineInputHeight - constFooterHeight - constSidebarHeight
		if m.pipelineScreen.contentVP.Height() != expectedContent {
			t.Errorf("width=%d: contentVP.Height=%d, want %d", w, m.pipelineScreen.contentVP.Height(), expectedContent)
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

// TestLayout_HeightInvariant_ContentModeTransition verifies that switching content
// modes (e.g. ContentStreaming → ContentPlanReview) does not push the footer
// off-screen. The root cause: ContentPlanReview uses a taller input zone
// (constPlanReviewInputHeight vs constPipelineInputHeight), so if viewports are
// not resized on transition the body overflows by exactly the height delta and
// the footer disappears.
func TestLayout_HeightInvariant_ContentModeTransition(t *testing.T) {
	sizes := [][2]int{{80, 24}, {120, 40}, {200, 60}}

	for _, sz := range sizes {
		w, h := sz[0], sz[1]
		t.Run(itoa(w)+"x"+itoa(h), func(t *testing.T) {
			// Start in streaming mode — viewports sized for constPipelineInputHeight.
			m := layoutTestModel(w, h, StatePipeline)

			// Simulate transition to plan review — the bug was missing recalculateLayout.
			m.pipelineScreen.content = ContentPlanReview
			m.pipelineScreen.hasPlan = true
			m.pipelineScreen.finalPlan = "# Plan\n\n## Goal\nDo the thing."
			ta := textarea.New()
			ta.SetWidth(max(1, w-4))
			ta.SetHeight(2)
			ta.CharLimit = 1024
			ta.Focus()
			m.pipelineScreen.planComment = ta
			m.pipelineScreen.hasPlanComment = true

			// The fix: recalculateLayout after content mode transition,
			// matching what the OrchestratorEventMsg / handlePipelineKey
			// handlers now do via planReviewHeightChanged.
			m.recalculateLayout()
			m.pipelineScreen.SyncViewports()

			// View must still fill exactly h lines — footer must not disappear.
			view := m.View().Content
			got := lipgloss.Height(view)
			if got != h {
				t.Errorf("after ContentStreaming→ContentPlanReview: lipgloss.Height = %d, want %d (footer disappeared)", got, h)
			}
		})
	}
}

// TestLayout_HeightInvariant_ErrorStateCompletion verifies that ContentCompletion
// with an active lastErr also preserves the exact terminal height.
func TestLayout_HeightInvariant_ErrorStateCompletion(t *testing.T) {
	m := layoutTestModel(120, 40, StatePipeline)
	m.pipelineScreen.content = ContentCompletion
	m.pipelineScreen.lastErr = fmt.Errorf("worker failed: exit status 1")

	view := m.View().Content
	got := lipgloss.Height(view)
	if got != 40 {
		t.Errorf("ContentCompletion+lastErr: lipgloss.Height = %d, want 40", got)
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
