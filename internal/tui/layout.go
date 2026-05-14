package tui

import (
	"image"
	"strings"

	"charm.land/lipgloss/v2"
)

// Layout constants — design constraints, not magic numbers.
const (
	minWidth  = 60 // below this, layout is physically impossible
	minHeight = 10 // below this, show a "terminal too small" message

	constFooterHeight = 2 // divider + key hints

	// Pipeline mode: divider + status line (the separator "\n" between body
	// and input is the body zone's line terminator, not chrome).
	constPipelineInputHeight = 2

	// Plan review mode: divider + 2-line comment textarea + padding
	constPlanReviewInputHeight = 4

	// Prompt mode: divider + instruction label + 3-line textarea
	constPromptInputHeight = 5

	// Run detail lower pane height (raw agent JSONL log)
	constRunLogHeight = 8

	// Content pane inset — 1-char left padding on each side.
	constContentInset = 2

	// Default context window size for the dashboard progress bar.
	constDefaultContextWindow = 200_000

	// Bottom sidebar height (agent list strip below the input zone).
	constSidebarHeight = 6

	// Number of raw stream lines to preview below activities
	streamPreviewLines = 5
)

// layoutBounds holds the computed bounding rectangles for each zone.
type layoutBounds struct {
	content  image.Rectangle
	sidebar  image.Rectangle
	textarea image.Rectangle
}

// renderPrefixedText hard-wraps text into the available width, applying style
// per segment. The first segment is prefixed with prefix; continuations are
// indented by the same number of spaces. Wrapping is done on raw bytes before
// any ANSI escape codes are injected, so style is applied per-segment.
func renderPrefixedText(style lipgloss.Style, prefix, text string, maxW int) string {
	wrapW := max(1, maxW-len(prefix))
	indent := strings.Repeat(" ", len(prefix))
	var b strings.Builder
	for i, rawLine := range strings.Split(text, "\n") {
		cur := prefix
		if i > 0 {
			cur = indent
		}
		for len(rawLine) > wrapW {
			b.WriteString(cur)
			b.WriteString(style.Render(rawLine[:wrapW]))
			b.WriteString("\n")
			rawLine = rawLine[wrapW:]
			cur = indent
		}
		b.WriteString(cur)
		b.WriteString(style.Render(rawLine))
		b.WriteString("\n")
	}
	return b.String()
}
