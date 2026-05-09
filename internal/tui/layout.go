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
)

// layoutBounds holds the computed bounding rectangles for each zone.
type layoutBounds struct {
	content  image.Rectangle
	sidebar  image.Rectangle
	textarea image.Rectangle
}

// recalculateLayout computes viewport and textarea dimensions based on current
// terminal size and state. Must be called from Update() after size changes.
func (m *Model) recalculateLayout() {
	if m.width < minWidth || m.height < minHeight {
		return
	}

	var inputHeight int
	switch m.state {
	case StatePrompt:
		inputHeight = constPromptInputHeight
	case StatePipeline:
		if m.content == ContentPlanReview {
			inputHeight = constPlanReviewInputHeight
		} else {
			inputHeight = constPipelineInputHeight
		}
	}

	usedHeight := constHeaderHeight + inputHeight + constFooterHeight
	contentHeight := max(0, m.height-usedHeight)
	contentWidth := max(0, int(float64(m.width)*splitRatio))
	sidebarWidth := max(0, m.width-contentWidth-1) // -1 for separator

	// Update viewports
	m.contentVP.Width = contentWidth
	m.contentVP.Height = contentHeight
	m.sidebarVP.Width = sidebarWidth
	m.sidebarVP.Height = contentHeight
	m.dashboardVP.Width = m.width
	m.dashboardVP.Height = contentHeight

	// Update textarea width for prompt mode
	if m.state == StatePrompt {
		m.prompt.SetWidth(max(1, m.width-4))
	}

	// Store bounding boxes for mouse event routing
	m.bounds = layoutBounds{
		content: image.Rect(0, constHeaderHeight, contentWidth, constHeaderHeight+contentHeight),
		sidebar: image.Rect(contentWidth+1, constHeaderHeight, m.width, constHeaderHeight+contentHeight),
		textarea: image.Rect(
			0,
			m.height-constFooterHeight-inputHeight,
			m.width,
			m.height-constFooterHeight,
		),
	}
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
