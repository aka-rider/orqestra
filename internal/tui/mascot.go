package tui

import (
	_ "embed"
	"strings"
)

//go:embed mascot.txt
var mascotArt string

// renderMascot returns the mascot ASCII art, trimmed to fit within the given
// width and height constraints.
func renderMascot(maxWidth, maxHeight int) string {
	lines := strings.Split(mascotArt, "\n")

	// Trim to height
	if len(lines) > maxHeight {
		lines = lines[:maxHeight]
	}

	// Trim each line to width
	for i, line := range lines {
		runes := []rune(line)
		if len(runes) > maxWidth {
			runes = runes[:maxWidth]
		}
		lines[i] = string(runes)
	}

	return strings.Join(lines, "\n")
}
