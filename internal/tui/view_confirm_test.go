package tui

import (
	"strings"
	"testing"
)

func TestConfirmView_DefaultCursorVisible(t *testing.T) {
	cv := newConfirmView()
	if !cv.cursorVisible {
		t.Error("newConfirmView() should set cursorVisible=true so cursor appears immediately on mount")
	}
}

func TestConfirmView_CursorBlinkToggle(t *testing.T) {
	cv := newConfirmView() // cursorVisible starts true

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
	cv := newConfirmView()
	cv.cursorVisible = true
	if !strings.Contains(cv.View(), "▌") {
		t.Error("View() should contain cursor glyph '▌' when cursorVisible=true")
	}
}

func TestConfirmView_ViewHidesCursorGlyphWhenInvisible(t *testing.T) {
	cv := newConfirmView()
	cv.cursorVisible = false
	if strings.Contains(cv.View(), "▌") {
		t.Error("View() should not contain cursor glyph '▌' when cursorVisible=false")
	}
}
