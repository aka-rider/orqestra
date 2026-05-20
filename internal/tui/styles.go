package tui

import "charm.land/lipgloss/v2"

var (
	// Header styles
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15"))

	phaseStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("14")).
			Bold(true)

	elapsedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244"))

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	// Content styles
	goalStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15")).
			Bold(true)

	stepStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("7"))

	passStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("2"))

	warnStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("3"))

	failStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("1"))

	// Input/footer styles
	keyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("7"))

	dividerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("1")).
			Bold(true)

	streamStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("7"))

	streamBlockStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("7")).
				Padding(0, 1)

	streamHintStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("8")) // dim

	// Runs history styles
	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("0")).
			Background(lipgloss.Color("14")).
			Bold(true)

	// Activity log styles
	activityToolStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("244")).
				Faint(true)

	activityPathStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("12"))

	activityDetailStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("244")).
				Faint(true)

	// File picker styles
	fpQueryStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("12")).
			Bold(true)

	fpSelectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("0")).
			Background(lipgloss.Color("12")).
			Bold(true)

	fpDimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244"))

	fpStatusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")).
			Faint(true)

	fpBorderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("12"))

	// Question (AskUserQuestion) styles
	questionHintStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("240")).
				Faint(true)

	questionGutterStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("240"))
)
