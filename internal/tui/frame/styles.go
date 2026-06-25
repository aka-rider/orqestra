package frame

import "charm.land/lipgloss/v2"

// Styles is the frame palette — which lipgloss style each frame renders with.
// Frames read it so they own their own look: a constructor takes no style
// argument, and changing a frame's appearance (or adding a frame) is a one-line
// edit to the palette, never a sweep of call sites.
//
// It is installed once at startup via SetStyles — a theme, i.e. write-once
// configuration set before any frame is built, matching how the rest of the TUI
// holds styles as package-level values. It is never mutated afterwards.
type Styles struct {
	Prose    lipgloss.Style
	Steer    lipgloss.Style
	Summary  lipgloss.Style
	Phase    lipgloss.Style
	Question lipgloss.Style
	Answer   lipgloss.Style
	Live     lipgloss.Style
	Tool     ToolStyles
}

var theme Styles

// SetStyles installs the frame palette. Call once before any frame is built.
func SetStyles(s Styles) { theme = s }
