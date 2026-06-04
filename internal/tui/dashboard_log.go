package tui

import (
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// LogViewerModel manages the bottom pane log display in the dashboard.
type LogViewerModel struct {
	vp          viewport.Model
	lines       []string
	isStreaming bool
	animFrame   int
	focused     bool

	width  int
	height int
}

// NewLogViewerModel creates a fresh log viewer.
func NewLogViewerModel() LogViewerModel {
	vp := viewport.New()
	vp.MouseWheelEnabled = true
	return LogViewerModel{vp: vp}
}

// SetSize updates dimensions.
func (l *LogViewerModel) SetSize(w, h int) {
	l.width = w
	l.height = h
	l.vp.SetWidth(w)
	l.vp.SetHeight(max(1, h))
}

// SetLines sets the log content.
func (l *LogViewerModel) SetLines(lines []string, streaming bool) {
	l.lines = lines
	l.isStreaming = streaming
	l.syncContent()
}

// AppendLines adds new lines to the log (for live streaming).
func (l *LogViewerModel) AppendLines(lines []string) {
	l.lines = append(l.lines, lines...)
	l.syncContent()
}

func (l *LogViewerModel) syncContent() {
	var content string
	if len(l.lines) > 0 {
		content = strings.Join(l.lines, "\n")
	}
	if l.isStreaming && len(spinningFrames) > 0 {
		spin := spinningFrames[l.animFrame%len(spinningFrames)]
		content += "\n" + dimStyle.Render("── "+spin+" ──")
	}
	atBottom := l.vp.AtBottom()
	l.vp.SetContent(content)
	if atBottom {
		l.vp.GotoBottom()
	}
}

// Update handles scroll keys when focused.
func (l LogViewerModel) Update(msg tea.Msg) (LogViewerModel, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return l, nil
	}

	var cmd tea.Cmd
	l.vp, cmd = l.vp.Update(key)
	return l, cmd
}

// View renders the log pane.
func (l LogViewerModel) View() string {
	if len(l.lines) == 0 && !l.isStreaming {
		placeholder := " Select an agent to view logs"
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")).
			Width(l.width).
			Height(l.height).
			Render(placeholder)
	}
	return l.vp.View()
}

// Ensure dimStyle and spinningFrames are accessible (they're defined in screen_pipeline.go)
var _ = lipgloss.NewStyle // ensure import used
