package tui

import (
	"charm.land/lipgloss/v2"

	"github.com/xiii/orqestra/internal/tui/frame"
)

// init installs the frame palette once at package load — the single place each
// frame's appearance is defined. Frames read it, so no constructor takes a style
// and changing a look is a one-line edit here. (streamSpeech/streamTool* live in
// timeline_view.go; all package-level style vars are initialised before init.)
func init() {
	frame.SetStyles(frame.Styles{
		Prose:     lipgloss.NewStyle(),
		Steer:     dimStyle,
		Summary:   phaseStyle,
		Phase:     dividerStyle,
		Question:  phaseStyle,
		Answer:    dimStyle,
		Live:      streamSpeechStyle,
		Collapsed: dimStyle,
		Tool: frame.ToolStyles{
			Pending: streamToolPendingStyle,
			OK:      streamToolOKStyle,
			Err:     streamToolErrStyle,
			Unknown: dimStyle,
		},
	})
}

var (
	// Header styles
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15"))

	phaseStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("14")).
			Bold(true)

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	// Content styles
	goalStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15")).
			Bold(true)

	passStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("2"))

	warnStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("3"))

	// Input/footer styles
	keyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("7"))

	dividerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("1")).
			Bold(true)

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

	// Question (AskUserQuestion) styles
	questionHintStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("240")).
				Faint(true)

	questionGutterStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("240"))
)

// selectionBg is the ANSI 256-colour index used for text-selection highlighting
// in the alt-screen transcript.
const selectionBg = "238"
