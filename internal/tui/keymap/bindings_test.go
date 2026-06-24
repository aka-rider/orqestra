package keymap

import (
	"testing"
	"unicode"
	"unicode/utf8"
)

// The shipped key map must never bind one physical key to two actions.
func TestDefaultNoPhysicalKeyCollisions(t *testing.T) {
	if err := Default().ValidateNoPhysicalKeyCollisions(); err != nil {
		t.Fatalf("default key map has a collision: %v", err)
	}
}

// Help is derived from the bindings by reflection; it must be non-empty and
// every entry must carry both a key glyph and a description.
func TestAllHelpDerivedAndComplete(t *testing.T) {
	help := Default().AllHelp()
	if len(help) == 0 {
		t.Fatal("AllHelp returned no entries")
	}
	for _, h := range help {
		if h.Key == "" || h.Desc == "" {
			t.Errorf("incomplete help entry: %+v", h)
		}
	}
}

// Non-printable-globals rule: no binding may capture a bare printable rune —
// printable input always belongs to the focused Chat. A binding key is legal
// only if it is a named/control key (length > 1, e.g. "enter", "pgup") or a
// modifier chord (contains '+').
func TestNoPrintableGlobals(t *testing.T) {
	for _, k := range Default().AllPhysicalKeys() {
		if utf8.RuneCountInString(k) == 1 {
			r, _ := utf8.DecodeRuneInString(k)
			if unicode.IsGraphic(r) && !unicode.IsSpace(r) {
				t.Errorf("printable key %q is bound globally; it must belong to Chat", k)
			}
		}
	}
}
