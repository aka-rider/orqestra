package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockPTYWriter records all writes and resize calls for testing.
type mockPTYWriter struct {
	written [][]byte
	resized []struct{ cols, rows uint }
}

func (m *mockPTYWriter) Write(p []byte) (int, error) {
	cp := make([]byte, len(p))
	copy(cp, p)
	m.written = append(m.written, cp)
	return len(p), nil
}

func (m *mockPTYWriter) Resize(cols, rows uint) error {
	m.resized = append(m.resized, struct{ cols, rows uint }{cols, rows})
	return nil
}

func TestTermView_ANSIRendering(t *testing.T) {
	tv := newTermView(0, 80, 25)

	// Feed ANSI-colored text.
	msg := PTYOutputMsg{TabIndex: 0, Data: []byte("\x1b[32mhello\x1b[0m world")}
	tv, _ = tv.Update(msg)

	output := tv.View()
	assert.Contains(t, output, "hello")
	assert.Contains(t, output, "world")
}

func TestTermView_BellConsumed(t *testing.T) {
	tv := newTermView(0, 80, 25)

	// Feed bell character — should not render as ^G.
	msg := PTYOutputMsg{TabIndex: 0, Data: []byte("before\aafter")}
	tv, _ = tv.Update(msg)

	output := tv.View()
	assert.Contains(t, output, "before")
	assert.Contains(t, output, "after")
	assert.NotContains(t, output, "^G")
}

func TestTermView_CursorMovement(t *testing.T) {
	tv := newTermView(0, 80, 25)

	// Move cursor to row 2, col 5 and write "hello".
	msg := PTYOutputMsg{TabIndex: 0, Data: []byte("\x1b[2;5Hhello")}
	tv, _ = tv.Update(msg)

	output := tv.vt.String()
	assert.Contains(t, output, "hello")
}

func TestTermView_Resize(t *testing.T) {
	tv := newTermView(0, 80, 25)

	tv, _ = tv.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	assert.Equal(t, 120, tv.width)
	assert.Equal(t, 40, tv.height)
	assert.Equal(t, 120, tv.vt.Width())
	assert.Equal(t, 39, tv.vt.Height()) // height-1 for status bar
}

func TestTermView_KeystrokeForwarding(t *testing.T) {
	tv := newTermView(0, 80, 25)
	mock := &mockPTYWriter{}
	tv.AttachPTY(mock)
	tv.focused = true

	tests := []struct {
		name     string
		msg      tea.KeyMsg
		expected []byte
	}{
		{"Enter", tea.KeyMsg{Type: tea.KeyEnter}, []byte{'\r'}},
		{"Backspace", tea.KeyMsg{Type: tea.KeyBackspace}, []byte{0x7f}},
		{"Tab", tea.KeyMsg{Type: tea.KeyTab}, []byte{'\t'}},
		{"Escape", tea.KeyMsg{Type: tea.KeyEscape}, []byte{0x1b}},
		{"ArrowUp", tea.KeyMsg{Type: tea.KeyUp}, []byte{0x1b, '[', 'A'}},
		{"ArrowDown", tea.KeyMsg{Type: tea.KeyDown}, []byte{0x1b, '[', 'B'}},
		{"ArrowRight", tea.KeyMsg{Type: tea.KeyRight}, []byte{0x1b, '[', 'C'}},
		{"ArrowLeft", tea.KeyMsg{Type: tea.KeyLeft}, []byte{0x1b, '[', 'D'}},
		{"CtrlC", tea.KeyMsg{Type: tea.KeyCtrlC}, []byte{0x03}},
		{"CtrlD", tea.KeyMsg{Type: tea.KeyCtrlD}, []byte{0x04}},
		{"CtrlZ", tea.KeyMsg{Type: tea.KeyCtrlZ}, []byte{0x1a}},
		{"Rune_a", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}, []byte("a")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock.written = nil
			tv, _ = tv.Update(tt.msg)
			require.Len(t, mock.written, 1)
			assert.Equal(t, tt.expected, mock.written[0])
		})
	}
}

func TestTermView_KeystrokeNotForwardedWhenUnfocused(t *testing.T) {
	tv := newTermView(0, 80, 25)
	mock := &mockPTYWriter{}
	tv.AttachPTY(mock)
	tv.focused = false

	tv, _ = tv.Update(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Empty(t, mock.written)
}

func TestTermView_KeystrokeNotForwardedWhenDone(t *testing.T) {
	tv := newTermView(0, 80, 25)
	mock := &mockPTYWriter{}
	tv.AttachPTY(mock)
	tv.focused = true
	tv.done = true

	tv, _ = tv.Update(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Empty(t, mock.written)
}

func TestTermView_StatusLine(t *testing.T) {
	t.Run("Running", func(t *testing.T) {
		tv := newTermView(0, 80, 25)
		output := tv.View()
		assert.Contains(t, output, "Running")
	})

	t.Run("NeedsInput", func(t *testing.T) {
		tv := newTermView(0, 80, 25)
		tv.needsInput = true
		output := tv.View()
		assert.Contains(t, output, "Waiting for input")
	})

	t.Run("Done_Success", func(t *testing.T) {
		tv := newTermView(0, 80, 25)
		tv.done = true
		tv.exitCode = 0
		output := tv.View()
		assert.Contains(t, output, "Exited (code 0)")
		assert.Contains(t, output, "✓")
	})

	t.Run("Done_Failed", func(t *testing.T) {
		tv := newTermView(0, 80, 25)
		tv.done = true
		tv.exitCode = 1
		output := tv.View()
		assert.Contains(t, output, "Exited (code 1)")
		assert.Contains(t, output, "✗")
	})
}

func TestTermView_IgnoresOtherTabIndex(t *testing.T) {
	tv := newTermView(0, 80, 25)

	// Messages for tab 1 should be ignored by tab 0's view.
	tv, _ = tv.Update(PTYOutputMsg{TabIndex: 1, Data: []byte("other tab")})
	output := tv.vt.String()
	assert.NotContains(t, output, "other tab")

	tv, _ = tv.Update(PTYNeedsInputMsg{TabIndex: 1})
	assert.False(t, tv.needsInput)

	tv, _ = tv.Update(PTYDoneMsg{TabIndex: 1, ExitCode: 0})
	assert.False(t, tv.done)
}

func TestTermView_ResizeForwardsToPTY(t *testing.T) {
	tv := newTermView(0, 80, 25)
	mock := &mockPTYWriter{}
	tv.AttachPTY(mock)

	tv, _ = tv.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	require.Len(t, mock.resized, 1)
	assert.Equal(t, uint(120), mock.resized[0].cols)
	assert.Equal(t, uint(39), mock.resized[0].rows) // height-1 for status bar
}
