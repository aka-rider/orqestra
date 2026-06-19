package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"rune/pkg/ui/components/textedit"
)

// makeTestInput builds a PromptInput backed by a real runeUI.
func makeTestInput(t *testing.T) PromptInput {
	t.Helper()
	ui, err := newRuneUI()
	if err != nil {
		t.Fatalf("newRuneUI: %v", err)
	}
	return newPromptInput(ui)
}

// typeRune sends a single printable rune to a PromptInput.
func typeRune(p PromptInput, ch string) PromptInput {
	msg := tea.KeyPressMsg{Text: ch}
	p, _ = p.Update(msg)
	return p
}

// TestValueRoundtrip checks that small typed text is returned by Value() unchanged.
func TestValueRoundtrip(t *testing.T) {
	p := makeTestInput(t)
	p = typeRune(p, "h")
	p = typeRune(p, "i")
	if got := p.Value(); got != "hi" {
		t.Errorf("Value() = %q, want %q", got, "hi")
	}
}

// TestPasteInline checks that a small paste (≤ threshold lines) is inserted verbatim.
func TestPasteInline(t *testing.T) {
	p := makeTestInput(t)
	small := "line1\nline2\nline3"
	p, _ = p.Update(tea.PasteMsg{Content: small})
	if got := p.Value(); got != small {
		t.Errorf("Value() = %q, want %q", got, small)
	}
}

// TestPasteChipExpansion checks that a large paste becomes a sentinel chip,
// then Value() expands it back to the original text.
func TestPasteChipExpansion(t *testing.T) {
	p := makeTestInput(t)
	large := strings.Repeat("line\n", pasteThreshold+1)
	p, _ = p.Update(tea.PasteMsg{Content: large})

	raw := p.Content()
	if !strings.ContainsRune(raw, sentinelStart) {
		t.Fatalf("expected sentinel in buffer after large paste, got %q", raw)
	}

	if got := p.Value(); got != large {
		t.Errorf("Value() mismatch after chip expansion:\ngot  %q\nwant %q", got, large)
	}
}

// TestBackspaceDeletesChip checks that Backspace with the cursor just after a
// chip sentinel atomically removes the entire chip.
func TestBackspaceDeletesChip(t *testing.T) {
	p := makeTestInput(t)
	large := strings.Repeat("line\n", pasteThreshold+1)
	p, _ = p.Update(tea.PasteMsg{Content: large})

	backspace := tea.KeyPressMsg{Code: tea.KeyBackspace}
	p, _ = p.Update(backspace)

	if strings.ContainsRune(p.Content(), sentinelStart) {
		t.Errorf("sentinel still present after Backspace: %q", p.Content())
	}
	if p.Value() != "" {
		t.Errorf("Value() = %q after chip deletion, want empty", p.Value())
	}
	if len(p.pastes) != 0 {
		t.Errorf("pastes map not cleared after chip deletion: %v", p.pastes)
	}
}

// TestPrintableInsideChipSwallowed checks that typing a printable key while the
// cursor is inside a chip sentinel's byte range has no effect on the buffer.
// Cursor moves by rune, so KeyLeft from after the chip (byte 7) lands at byte 4
// — inside the sentinelEnd rune's byte range — where our pre-emption triggers.
func TestPrintableInsideChipSwallowed(t *testing.T) {
	p := makeTestInput(t)
	large := strings.Repeat("line\n", pasteThreshold+1)
	p, _ = p.Update(tea.PasteMsg{Content: large})

	rawBefore := p.Content()
	// Move cursor back one rune — inside the sentinelEnd rune's bytes.
	p, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyLeft})

	cur := p.CursorOffset()
	chips := p.findChips()
	if len(chips) == 0 {
		t.Fatal("no chips found")
	}
	if cur <= chips[0].start || cur >= chips[0].end {
		t.Skipf("cursor %d not inside chip [%d, %d) — cannot test swallow",
			cur, chips[0].start, chips[0].end)
	}

	p = typeRune(p, "x")
	if got := p.Content(); got != rawBefore {
		t.Errorf("buffer mutated after typing inside chip:\nbefore: %q\nafter:  %q", rawBefore, got)
	}
}

// TestFindChipsNone checks that findChips returns nil for a plain buffer.
func TestFindChipsNone(t *testing.T) {
	p := makeTestInput(t)
	p = typeRune(p, "h")
	p = typeRune(p, "i")
	if chips := p.findChips(); len(chips) != 0 {
		t.Errorf("findChips() = %v, want nil", chips)
	}
}

// TestFindChipsMultiple checks that two chips placed via handlePaste are found
// in ascending byte order.
func TestFindChipsMultiple(t *testing.T) {
	p := makeTestInput(t)
	large := strings.Repeat("line\n", pasteThreshold+1)

	// Insert via sequential pastes; cursor must advance past each chip so the
	// second paste appends rather than nesting inside the first.
	p, _ = p.Update(tea.PasteMsg{Content: large})
	// Verify cursor is after the chip before the second paste.
	if p.CursorOffset() == 0 {
		t.Fatal("cursor at 0 after first paste — second paste would corrupt first chip")
	}
	p, _ = p.Update(tea.PasteMsg{Content: large})

	chips := p.findChips()
	if len(chips) != 2 {
		t.Fatalf("findChips() = %d chips, want 2", len(chips))
	}
	if chips[0].start >= chips[1].start {
		t.Errorf("chips not in ascending order: %v", chips)
	}
}

// TestResetClearsAll checks that Reset() clears the paste map, content, and ID counter.
func TestResetClearsAll(t *testing.T) {
	p := makeTestInput(t)
	large := strings.Repeat("line\n", pasteThreshold+1)
	p, _ = p.Update(tea.PasteMsg{Content: large})
	p = p.Reset()

	if p.nextID != 0 {
		t.Errorf("nextID = %d after Reset(), want 0", p.nextID)
	}
	if len(p.pastes) != 0 {
		t.Errorf("pastes = %v after Reset(), want empty", p.pastes)
	}
	if p.Value() != "" {
		t.Errorf("Value() = %q after Reset(), want empty", p.Value())
	}
}

// TestRenderPurity checks that repeated View() calls with the same state produce
// identical output (pure render — no hidden mutable side effects).
func TestRenderPurity(t *testing.T) {
	ui, err := newRuneUI()
	if err != nil {
		t.Fatalf("newRuneUI: %v", err)
	}
	p := newPromptInput(ui)
	p = p.SetRect(textedit.Rect{W: 40, H: 10})
	p = typeRune(p, "h")
	p = typeRune(p, "i")

	first := p.View()
	second := p.View()
	if first != second {
		t.Errorf("View() not pure:\nfirst:  %q\nsecond: %q", first, second)
	}
}
