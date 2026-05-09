package tui

import "github.com/charmbracelet/lipgloss"

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

	// Activity bar styles
	activityIconStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("3")).
				Faint(true)

	activityToolStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("14")).
				Faint(true).
				Bold(true)

	activityDetailStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("244")).
				Faint(true)

	activitySepStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("240"))
)
