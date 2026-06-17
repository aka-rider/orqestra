package tui

import (
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

	// Prompt mode: divider + instruction label + 3-line textarea
	constPromptInputHeight = 5

	// Run detail lower pane height (raw agent JSONL log)
	constRunLogHeight = 8

	// Run detail header: status line + horizontal divider.
	constRunDetailHeaderHeight = 2

	// Run detail right-pane chrome: header(2) + input-label(1) + output-separator(1) + footer(2).
	constRunDetailChromeHeight = 6

	// Run detail left pane (agent menu) as percentage of terminal width.
	constRunDetailMenuPct = 30

	// Run detail minimum width for agent card pane (ensures card borders fit).
	constRunDetailMinMenuW = 30

	// Content pane inset — 1-char left padding on each side.
	constContentInset = 2

	// Bottom sidebar height (agent list strip below the input zone).
	constSidebarHeight = 1
)

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

// deduplicateLines removes earlier occurrences of any line that appears later,
// preserving original order of last occurrences. Used for stream display.
func deduplicateLines(lines []string) []string {
	seen := make(map[string]struct{}, len(lines))
	out := make([]string, 0, len(lines))
	for i := len(lines) - 1; i >= 0; i-- {
		if _, dup := seen[lines[i]]; !dup {
			seen[lines[i]] = struct{}{}
			out = append(out, lines[i])
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
