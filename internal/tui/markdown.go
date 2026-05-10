package tui

import "charm.land/glamour/v2"

// renderMarkdown renders a markdown string using glamour for styled terminal
// output. Falls back to raw content on any rendering error.
func renderMarkdown(content string, width int) string {
	r, err := glamour.NewTermRenderer(
		glamour.WithWordWrap(max(20, width-4)),
		glamour.WithEmoji(),
	)
	if err != nil {
		return content
	}
	out, err := r.Render(content)
	if err != nil {
		return content
	}
	return out
}
