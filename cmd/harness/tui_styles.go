package main

import (
	"fmt"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	"charm.land/lipgloss/v2"
)

var (
	// Style definitions adapted from internal/tui/styles.go.

	dividerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("14")).
			Bold(true)

	inputStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15"))

	footerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")).
			Faint(true)

	toolStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("12"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("1")).
			Bold(true)

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")).
			Faint(true)

	goalStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15")).
			Bold(true)
)

// renderMarkdown renders markdown using glamour with the same style config
// as the Orqestra TUI. Falls back to raw content on any rendering error.
func renderMarkdown(content string, width int) string {
	r, err := glamour.NewTermRenderer(
		glamour.WithWordWrap(max(20, width-4)),
		glamour.WithEmoji(),
		glamour.WithStyles(glamourStyleConfig()),
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

// glamourStyleConfig returns a rich style config for markdown rendering.
// Copied from internal/tui/markdown.go to keep the harness self-contained.
func glamourStyleConfig() ansi.StyleConfig {
	return ansi.StyleConfig{
		Document: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: strPtr(rgb(153, 153, 153)),
			},
		},
		BlockQuote: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: strPtr(rgb(160, 180, 200)),
			},
			Indent: uintPtr(2),
		},
		Paragraph: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: strPtr(rgb(220, 220, 220)),
			},
		},
		Heading:   ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: boolPtr(true)}},
		H1:        ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: strPtr(rgb(202, 158, 234)), Bold: boolPtr(true)}},
		H2:        ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: strPtr(rgb(150, 180, 220)), Bold: boolPtr(true)}},
		H3:        ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: strPtr(rgb(160, 190, 230)), Bold: boolPtr(true)}},
		H4:        ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: strPtr(rgb(170, 200, 240)), Bold: boolPtr(true)}},
		H5:        ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: strPtr(rgb(180, 210, 240)), Bold: boolPtr(true)}},
		H6:        ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: strPtr(rgb(190, 220, 240)), Bold: boolPtr(true)}},
		Strong:    ansi.StylePrimitive{Bold: boolPtr(true), Color: strPtr(rgb(255, 255, 255))},
		Emph:      ansi.StylePrimitive{Italic: boolPtr(true), Color: strPtr(rgb(220, 220, 220))},
		Strikethrough: ansi.StylePrimitive{Color: strPtr(rgb(150, 150, 150))},
		HorizontalRule: ansi.StylePrimitive{
			Color: strPtr(rgb(100, 100, 100)),
		},
		Code: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color:           strPtr(rgb(255, 121, 121)),
				BackgroundColor: strPtr(rgb(25, 25, 25)),
			},
		},
		CodeBlock: ansi.StyleCodeBlock{
			StyleBlock: ansi.StyleBlock{
				StylePrimitive: ansi.StylePrimitive{
					Color:           strPtr(rgb(180, 200, 240)),
					BackgroundColor: strPtr(rgb(20, 20, 30)),
				},
			},
		},
		List: ansi.StyleList{
			StyleBlock: ansi.StyleBlock{
				StylePrimitive: ansi.StylePrimitive{
					Color: strPtr(rgb(180, 200, 240)),
				},
			},
		},
		Item:        ansi.StylePrimitive{Color: strPtr(rgb(180, 200, 240))},
		Enumeration: ansi.StylePrimitive{Color: strPtr(rgb(180, 200, 240))},
		Task:        ansi.StyleTask{StylePrimitive: ansi.StylePrimitive{Color: strPtr(rgb(180, 200, 240))}},
		Link:        ansi.StylePrimitive{Color: strPtr(rgb(100, 160, 240)), Underline: boolPtr(true)},
		LinkText:    ansi.StylePrimitive{Color: strPtr(rgb(130, 180, 240)), Underline: boolPtr(true)},
		Image:       ansi.StylePrimitive{Color: strPtr(rgb(180, 200, 240))},
	}
}

// Helper functions copied from internal/tui/markdown.go.
func strPtr(s string) *string   { return &s }
func boolPtr(b bool) *bool      { return &b }
func uintPtr(u uint) *uint      { return &u }
func rgb(r, g, b uint16) string {
	return fmt.Sprintf("rgb:%02x%02x%02x", r, g, b)
}
