//go:build darwin

package mux

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestScanForBEL(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		wantBELs int
	}{
		{
			name:     "no BEL",
			data:     []byte("hello world"),
			wantBELs: 0,
		},
		{
			name:     "standalone BEL",
			data:     []byte("hello\x07world"),
			wantBELs: 1,
		},
		{
			name:     "BEL inside OSC (ignored)",
			data:     []byte("\x1b]0;title\x07"),
			wantBELs: 0,
		},
		{
			name:     "BEL after OSC terminated with ST",
			data:     []byte("\x1b]0;title\x1b\\\x07"),
			wantBELs: 1,
		},
		{
			name:     "multiple standalone BELs",
			data:     []byte("\x07\x07\x07"),
			wantBELs: 3,
		},
		{
			name:     "mixed: OSC BEL + standalone BEL",
			data:     []byte("\x1b]0;title\x07some text\x07"),
			wantBELs: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var count int
			scanForBEL(tt.data, func() { count++ })
			assert.Equal(t, tt.wantBELs, count)
		})
	}
}

func TestMuxConfig_DefaultPrefixKey(t *testing.T) {
	m := New(Config{})
	assert.Equal(t, byte(0x02), m.cfg.PrefixKey, "default prefix key should be Ctrl+B")
}

func TestMuxConfig_CustomPrefixKey(t *testing.T) {
	m := New(Config{PrefixKey: 0x1d}) // Ctrl+]
	assert.Equal(t, byte(0x1d), m.cfg.PrefixKey)
}

func TestIsPrefixKittySeq(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"standard Ctrl+B kitty", []byte("\x1b[98;5u"), true},
		{"Ctrl+B kitty press event", []byte("\x1b[98;5:1u"), true},
		{"Ctrl+B kitty repeat event", []byte("\x1b[98;5:2u"), true},
		{"Ctrl+B kitty with trailing data", []byte("\x1b[98;5ufoo"), true},
		{"not a kitty seq", []byte("\x1b[A"), false},
		{"too short", []byte("\x1b["), false},
		{"wrong codepoint", []byte("\x1b[97;5u"), false},
		{"no modifier", []byte("\x1b[98u"), false},
		{"raw 0x02", []byte{0x02}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isPrefixKittySeq(tt.data))
		})
	}
}
