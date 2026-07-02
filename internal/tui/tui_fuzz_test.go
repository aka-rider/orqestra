//go:build fuzz

package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// FuzzTUIInput feeds random byte sequences to the TUI model as event streams
// and checks that the model never panics and remains in a consistent state.
//
// Scope: prompt-screen navigation only. Enter/submit is excluded from the
// decoder to avoid triggering engine.Start, which launches a real subprocess.
// Pipeline-state fuzzing is deferred pending a no-op engine stub.
func FuzzTUIInput(f *testing.F) {
	// Seed: printable text input in prompt screen
	f.Add([]byte("hello world"))
	// Seed: Ctrl+R (runs list navigation)
	f.Add([]byte{0x12})
	// Seed: scroll wheel up + down
	f.Add([]byte{0x80, 0x00, 0x80, 0x01})
	// Seed: Ctrl+A (beginning of line)
	f.Add([]byte{0x01})
	// Seed: mixed prompt text + ctrl commands
	f.Add([]byte("test prompt\x12\x1b"))

	f.Fuzz(func(t *testing.T, input []byte) {
		m := testModel()
		for len(input) > 0 {
			ev, n := decodeFuzzEvent(input)
			input = input[n:]
			if ev == nil {
				continue
			}
			result, _ := m.Update(ev)
			m = result.(Model)
			checkFuzzInvariants(t, m)
		}
	})
}

// decodeFuzzEvent decodes one event from input and returns the event and bytes consumed.
// Returns (nil, 1) for byte values that are skipped (Enter, unknown).
func decodeFuzzEvent(input []byte) (tea.Msg, int) {
	b := input[0]
	switch {
	case b >= 0x01 && b <= 0x1A:
		// Ctrl+A through Ctrl+Z: Code is the corresponding letter, Mod=Ctrl.
		return tea.KeyPressMsg{Code: 'a' + rune(b-1), Mod: tea.ModCtrl}, 1

	case b == 0x1B:
		return tea.KeyPressMsg{Code: tea.KeyEscape}, 1

	case b == 0x1D:
		return tea.KeyPressMsg{Code: tea.KeyBackspace}, 1

	case b == 0x1E:
		return tea.KeyPressMsg{Code: tea.KeyTab}, 1

	case b >= 0x20 && b <= 0x7E:
		r := rune(b)
		return tea.KeyPressMsg{Code: r, Text: string(r)}, 1

	case b == 0x80:
		// Mouse wheel: next byte is direction (0=up, 1=down).
		if len(input) < 2 {
			return nil, 1
		}
		if input[1]%2 == 0 {
			return tea.MouseWheelMsg{Button: tea.MouseWheelUp}, 2
		}
		return tea.MouseWheelMsg{Button: tea.MouseWheelDown}, 2

	case b == 0x81:
		// Mouse click: next two bytes are x, y coords.
		if len(input) < 3 {
			return nil, 1
		}
		return tea.MouseClickMsg{
			Button: tea.MouseLeft,
			X:      int(input[1]) % 120,
			Y:      int(input[2]) % 40,
		}, 3

	case b == 0x82:
		// Mouse release: next two bytes are x, y coords.
		if len(input) < 3 {
			return nil, 1
		}
		return tea.MouseReleaseMsg{
			X: int(input[1]) % 120,
			Y: int(input[2]) % 40,
		}, 3

	case b == 0x83:
		// Window resize: next two bytes are w, h offsets.
		if len(input) < 3 {
			return nil, 1
		}
		return tea.WindowSizeMsg{
			Width:  int(input[1]) + 60,
			Height: int(input[2]) + 10,
		}, 3
	}

	// 0x1C (Enter/submit), 0x00, 0x7F, 0x84–0xFF: skip.
	return nil, 1
}

func checkFuzzInvariants(t *testing.T, m Model) {
	t.Helper()

	switch m.state {
	case StatePrompt, StatePipeline, StateRunsList, StateRunDetail:
	default:
		t.Fatalf("unknown app state %d", m.state)
	}

	if (m.obs != nil) != (m.cancelCause != nil) {
		t.Fatalf("obs/cancel set asymmetrically: obs=%v cancel=%v", m.obs != nil, m.cancelCause != nil)
	}

	// Timeline region bounds are only maintained while in StatePipeline.
	if m.state == StatePipeline {
		if m.regions.timeline.Max.X > m.width || m.regions.timeline.Max.Y > m.height {
			t.Fatalf("timeline region exceeds window: region=%v window=(%d,%d)",
				m.regions.timeline, m.width, m.height)
		}
	}
}
