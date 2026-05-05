package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Colors
	primaryColor   = lipgloss.Color("#7C3AED")
	secondaryColor = lipgloss.Color("#10B981")
	mutedColor     = lipgloss.Color("#6B7280")
	errorColor     = lipgloss.Color("#EF4444")
	bgColor        = lipgloss.Color("#1F2937")
	approveColor   = lipgloss.Color("#10B981")
	rejectColor    = lipgloss.Color("#EF4444")
	editColor      = lipgloss.Color("#F59E0B")

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

	attentionTabStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#F59E0B")).
				Border(lipgloss.RoundedBorder(), true, true, false, true).
				BorderForeground(lipgloss.Color("#F59E0B")).
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

	// Command bar styles
	commandBarInputStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#E5E7EB"))

	commandBarHintStyle = lipgloss.NewStyle().
				Foreground(mutedColor).
				Italic(true)

	// Autocomplete overlay styles
	acOverlayStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(primaryColor).
			Padding(0, 1)

	acItemStyle = lipgloss.NewStyle().
			Foreground(mutedColor)

	acSelectedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(primaryColor)

	// Approve/reject key styles
	approveKeyStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(approveColor)

	rejectKeyStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(rejectColor)

	editKeyStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(editColor)

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280")).
			Italic(true)

	// Confirm pane borders — width set dynamically in View, not here.
	stylePlanPane = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("240"))

	stylePlanPaneFocused = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderForeground(lipgloss.Color("69"))

	styleInputPane = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("240"))

	styleInputPaneFocused = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderForeground(lipgloss.Color("69"))

	// InputBoxStyle and InputBoxFocusedStyle wrap any interactive text-input
	// widget with a rounded border. Width must be set at render time.
	InputBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240"))

	InputBoxFocusedStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(primaryColor)
)
