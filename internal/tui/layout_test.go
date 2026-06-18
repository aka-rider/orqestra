package tui

import (
	"fmt"
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
				// Alt-screen: the view owns the screen and fills up to the full
				// height. Verify it does not overflow.
				if got > sz[1] {
					t.Errorf("lipgloss.Height(View()) = %d, overflows %d", got, sz[1])
				}
			})
		}
	}
}

func TestLayout_HeightInvariant_SmallTerminal(t *testing.T) {
	m := layoutTestModel(60, 10, StatePrompt)
	view := m.View().Content
	got := lipgloss.Height(view)
	if got > 10 {
		t.Errorf("prompt at 60x10: lipgloss.Height = %d, want <= 10", got)
	}

	m2 := layoutTestModel(60, 10, StatePipeline)
	view2 := m2.View().Content
	got2 := lipgloss.Height(view2)
	if got2 > 10 {
		t.Errorf("pipeline at 60x10: lipgloss.Height = %d, want <= 10", got2)
	}
}

// TestLayout_HeightInvariant_ErrorStateCompletion verifies that ContentCompletion
// with an active lastErr does not overflow the terminal height.
func TestLayout_HeightInvariant_ErrorStateCompletion(t *testing.T) {
	m := layoutTestModel(120, 40, StatePipeline)
	m.pipelineScreen.content = ContentCompletion
	m.pipelineScreen.lastErr = fmt.Errorf("worker failed: exit status 1")

	view := m.View().Content
	got := lipgloss.Height(view)
	if got > 40 {
		t.Errorf("ContentCompletion+lastErr: lipgloss.Height = %d, overflows 40", got)
	}
}

// TestLayout_AltScreenEnabled guards the alt-screen invariant. The in-app
// content viewport requires owning the screen; without alt-screen the program
// runs inline and scrolling is dead.
func TestLayout_AltScreenEnabled(t *testing.T) {
	states := []AppState{StatePrompt, StatePipeline, StateRunsList, StateRunDetail}
	for _, st := range states {
		m := layoutTestModel(120, 40, st)
		if st == StatePipeline {
			m.pipelineScreen.content = ContentStreaming
		}
		if !m.View().AltScreen {
			t.Errorf("expected AltScreen=true for state %d", st)
		}
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
