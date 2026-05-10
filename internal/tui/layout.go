package tui

import (
	"image"

	"github.com/charmbracelet/lipgloss"
)

// Layout constants — design constraints, not magic numbers.
const (
	splitRatio = 0.75

	minWidth  = 60 // below this, layout is physically impossible
	minHeight = 10 // below this, show a "terminal too small" message

	constHeaderHeight = 2 // title + divider
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
)

// layoutBounds holds the computed bounding rectangles for each zone.
type layoutBounds struct {
	content  image.Rectangle
	sidebar  image.Rectangle
	textarea image.Rectangle
}

// joinSplitView composes left and right panes with a border separator.
func joinSplitView(left, right string, leftWidth, rightWidth, height int) string {
	l := lipgloss.Place(leftWidth, height, lipgloss.Left, lipgloss.Top, left)

	sidebarStyle := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(lipgloss.Color("238")).
		Width(rightWidth).
		Height(height)

	r := sidebarStyle.Render(right)

	return lipgloss.JoinHorizontal(lipgloss.Top, l, r)
}
