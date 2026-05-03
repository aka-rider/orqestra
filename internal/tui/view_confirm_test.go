package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// initCV returns a confirmView with viewport ready at 80×24.
func initCV(t *testing.T) confirmView {
	t.Helper()
	cv := newConfirmView()
	cv, _ = cv.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return cv
}

func TestConfirmView_DefaultCursorVisible(t *testing.T) {
	cv := newConfirmView()
	if !cv.cursorVisible {
		t.Error("newConfirmView() should set cursorVisible=true so cursor appears immediately on mount")
	}
}

func TestConfirmView_CursorBlinkToggle(t *testing.T) {
	cv := newConfirmView()

	cv2, cmd := cv.Update(CursorBlinkMsg{})
	if cv2.cursorVisible {
		t.Error("after CursorBlinkMsg, cursorVisible should toggle from true to false")
	}
	if cmd == nil {
		t.Error("Update(CursorBlinkMsg{}) must return a non-nil re-arm cmd to continue the blink loop")
	}
}

func TestConfirmView_FocusReturnsBlinkCmd(t *testing.T) {
	cv := newConfirmView()
	cmd := cv.Focus()
	if cmd == nil {
		t.Fatal("Focus() should return non-nil blinkCmd to start the blink loop")
	}
}

func TestConfirmView_BlurClearsCursorVisible(t *testing.T) {
	cv := newConfirmView()
	_ = cv.Focus()
	cv.Blur()
	if cv.cursorVisible {
		t.Error("after Blur(), cursorVisible should be false")
	}
}

func TestConfirmView_ViewShowsCursorGlyphWhenVisible(t *testing.T) {
	cv := initCV(t)
	cv.cursorVisible = true
	if !strings.Contains(cv.View(), "▌") {
		t.Error("View() should contain cursor glyph '▌' when cursorVisible=true")
	}
}

func TestConfirmView_ViewHidesCursorGlyphWhenInvisible(t *testing.T) {
	cv := initCV(t)
	cv.cursorVisible = false
	if strings.Contains(cv.View(), "▌") {
		t.Error("View() should not contain cursor glyph '▌' when cursorVisible=false")
	}
}

// --- New tests ---

func TestConfirmViewport_ScrollsOnKeyDown(t *testing.T) {
	cv := newConfirmView()
	// Fill with enough lines so there is room to scroll.
	var lines strings.Builder
	for i := 0; i < 50; i++ {
		lines.WriteString("line content here\n")
	}
	cv.planText = lines.String()
	cv, _ = cv.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if !cv.ready {
		t.Fatal("viewport should be ready after WindowSizeMsg")
	}

	cv, _ = cv.Update(tea.KeyMsg{Type: tea.KeyDown})
	cv, _ = cv.Update(tea.KeyMsg{Type: tea.KeyDown})

	if cv.viewport.YOffset == 0 {
		t.Error("viewport YOffset should be > 0 after two Down key presses while focusPlan")
	}
}

func TestConfirmView_HasBorder(t *testing.T) {
	cv := initCV(t)
	out := cv.View()
	// NormalBorder uses │ and ─ box-drawing characters.
	if !strings.Contains(out, "─") && !strings.Contains(out, "│") {
		t.Error("View() should contain NormalBorder characters (─ or │)")
	}
}

func TestConfirmView_FocusHighlight(t *testing.T) {
	// Use lipgloss Style getters to verify border colors without relying on
	// ANSI rendering, which is disabled in non-TTY test environments.
	if stylePlanPaneFocused.GetBorderTopForeground() == stylePlanPane.GetBorderTopForeground() {
		t.Error("focused plan pane should have a different border color than unfocused")
	}
	if styleInputPaneFocused.GetBorderTopForeground() == styleInputPane.GetBorderTopForeground() {
		t.Error("focused input pane should have a different border color than unfocused")
	}

	// Both focused styles should share the same highlight color.
	if stylePlanPaneFocused.GetBorderTopForeground() != styleInputPaneFocused.GetBorderTopForeground() {
		t.Error("plan and input focused styles should use the same highlight color")
	}
	// Both unfocused styles should share the same dim color.
	if stylePlanPane.GetBorderTopForeground() != styleInputPane.GetBorderTopForeground() {
		t.Error("plan and input unfocused styles should use the same dim color")
	}

	// Functional check: View() renders without panic in both focus states.
	cv := initCV(t)
	_ = cv.View() // focusPlan
	cv.planFocused = false
	_ = cv.View() // focusInput
}

func TestConfirmView_ApproveKey(t *testing.T) {
	cv := initCV(t)
	_, cmd := cv.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatal("'y' key should return a ConfirmMsg cmd")
	}
	msg := cmd()
	cm, ok := msg.(ConfirmMsg)
	if !ok {
		t.Fatalf("expected ConfirmMsg, got %T", msg)
	}
	if cm.Choice != ConfirmAccept {
		t.Errorf("'y' key should produce ConfirmMsg{Choice: ConfirmAccept}, got %v", cm.Choice)
	}
}

func TestConfirmView_RejectKey(t *testing.T) {
	cv := initCV(t)
	_, cmd := cv.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if cmd == nil {
		t.Fatal("'n' key should return a ConfirmMsg cmd")
	}
	msg := cmd()
	cm, ok := msg.(ConfirmMsg)
	if !ok {
		t.Fatalf("expected ConfirmMsg, got %T", msg)
	}
	if cm.Choice != ConfirmReject {
		t.Errorf("'n' key should produce ConfirmMsg{Choice: ConfirmReject}, got %v", cm.Choice)
	}
}

func TestConfirmView_LoadingBeforeReady(t *testing.T) {
	cv := newConfirmView()
	out := cv.View()
	if !strings.Contains(out, "Loading") {
		t.Error("View() before WindowSizeMsg should show a loading message")
	}
}

func TestInputBoxStyles_ColorsAreDifferent(t *testing.T) {
	if InputBoxFocusedStyle.GetBorderTopForeground() == InputBoxStyle.GetBorderTopForeground() {
		t.Error("InputBoxFocusedStyle should have a different border color than InputBoxStyle")
	}
}

func TestConfirmView_InputPaneHasRoundedBorder(t *testing.T) {
	cv := initCV(t)
	out := cv.View()
	// RoundedBorder produces ╭ ╮ ╰ ╯ corner characters; at least one must appear.
	if !strings.ContainsAny(out, "╭╮╰╯") {
		t.Error("View() should contain rounded border corner characters (╭╮╰╯)")
	}
}
