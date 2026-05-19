package tui

import (
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ArtifactViewerModel manages the right pane with top/bottom viewports.
type ArtifactViewerModel struct {
	topVP    viewport.Model
	bottomVP viewport.Model

	topContent    string
	bottomContent string
	topPath       string
	bottomPath    string
	focusTop      bool // true = top VP has focus, false = bottom
	agentID       string

	width  int
	height int
}

// NewArtifactViewerModel creates a fresh artifact viewer.
func NewArtifactViewerModel() ArtifactViewerModel {
	top := viewport.New()
	top.MouseWheelEnabled = true
	bot := viewport.New()
	bot.MouseWheelEnabled = true
	return ArtifactViewerModel{
		topVP:    top,
		bottomVP: bot,
		focusTop: true,
	}
}

// SetSize updates dimensions and splits height 50/50.
func (a *ArtifactViewerModel) SetSize(w, h int) {
	a.width = w
	a.height = h
	topH := h / 2
	botH := h - topH - 1 // 1 for divider
	a.topVP.SetWidth(w)
	a.topVP.SetHeight(max(1, topH))
	a.bottomVP.SetWidth(w)
	a.bottomVP.SetHeight(max(1, botH))
}

// SetAgent updates which agent's artifacts are shown.
func (a *ArtifactViewerModel) SetAgent(id string) {
	a.agentID = id
	// Content will be set externally via SetContent
}

// SetContent sets the top and bottom viewport content.
func (a *ArtifactViewerModel) SetContent(top, bottom string) {
	a.topContent = top
	a.bottomContent = bottom
	a.topVP.SetContent(top)
	a.topVP.GotoTop()
	a.bottomVP.SetContent(bottom)
	a.bottomVP.GotoTop()
}

// Update handles scroll keys for the focused viewport.
func (a ArtifactViewerModel) Update(msg tea.Msg) (ArtifactViewerModel, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return a, nil
	}

	var cmd tea.Cmd
	if a.focusTop {
		a.topVP, cmd = a.topVP.Update(key)
	} else {
		a.bottomVP, cmd = a.bottomVP.Update(key)
	}
	return a, cmd
}

// View renders the top and bottom viewports with a divider.
func (a ArtifactViewerModel) View() string {
	divStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	topLabel := " Input"
	if a.focusTop {
		topLabel = "▶Input"
	}
	botLabel := " Output"
	if !a.focusTop {
		botLabel = "▶Output"
	}

	var b strings.Builder
	b.WriteString(divStyle.Render(topLabel))
	b.WriteString("\n")
	b.WriteString(a.topVP.View())
	b.WriteString("\n")
	b.WriteString(divStyle.Render(botLabel + " " + strings.Repeat("─", max(0, a.width-len(botLabel)-1))))
	b.WriteString("\n")
	b.WriteString(a.bottomVP.View())
	return b.String()
}
