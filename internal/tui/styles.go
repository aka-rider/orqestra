package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Colors
	primaryColor   = lipgloss.Color("#7C3AED")
	secondaryColor = lipgloss.Color("#10B981")
	mutedColor     = lipgloss.Color("#6B7280")
	errorColor     = lipgloss.Color("#EF4444")
	bgColor        = lipgloss.Color("#1F2937")

	// Heading styles
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(primaryColor).
			MarginBottom(1)

	subtitleStyle = lipgloss.NewStyle().
			Foreground(mutedColor).
			Italic(true)

	// Plan display
	goalStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(secondaryColor)

	stepStyle = lipgloss.NewStyle().
			PaddingLeft(2)

	acceptanceStyle = lipgloss.NewStyle().
			PaddingLeft(2).
			Foreground(secondaryColor)

	// Borders
	borderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(primaryColor).
			Padding(1, 2)

	// Tab styles
	activeTabStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(primaryColor).
			Border(lipgloss.RoundedBorder(), true, true, false, true).
			BorderForeground(primaryColor).
			Padding(0, 2)

	inactiveTabStyle = lipgloss.NewStyle().
				Foreground(mutedColor).
				Border(lipgloss.RoundedBorder(), true, true, false, true).
				BorderForeground(mutedColor).
				Padding(0, 2)

	tabGapStyle = lipgloss.NewStyle().
			Border(lipgloss.Border{Bottom: "─"}, false, false, true, false).
			BorderForeground(mutedColor)

	// Confirm prompt
	confirmStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(primaryColor)

	// Error
	errorStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(errorColor)

	// Status bar
	statusStyle = lipgloss.NewStyle().
			Foreground(mutedColor).
			Italic(true)
)
