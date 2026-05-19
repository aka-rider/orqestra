package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// DashboardFocus identifies which pane currently has keyboard focus.
type DashboardFocus int

const (
	FocusMenu      DashboardFocus = iota // left pane: agent list
	FocusArtTop                          // right pane: top viewport (input/prompt)
	FocusArtBottom                       // right pane: bottom viewport (output/plan)
	FocusLog                             // bottom pane: streaming log
)

// CloseDashboardIntent is emitted when Esc is pressed from the menu pane.
type CloseDashboardIntent struct{}

func (CloseDashboardIntent) isIntent() {}

// agentSelectedMsg is emitted by the menu when the cursor moves to a new agent.
type agentSelectedMsg struct{ id string }

// DashboardModel is the FSM controller for the 3-pane dashboard overlay.
// It manages sub-components: AgentMenuModel (left), ArtifactViewerModel (right),
// and LogViewerModel (bottom). All layout and msg routing is owned here.
type DashboardModel struct {
	focus  DashboardFocus
	width  int
	height int

	menu     AgentMenuModel
	artifact ArtifactViewerModel
	log      LogViewerModel

	PendingIntent tea.Msg
}

// NewDashboardModel creates a fresh dashboard in menu-focused state.
func NewDashboardModel() DashboardModel {
	return DashboardModel{
		focus:    FocusMenu,
		menu:     NewAgentMenuModel(),
		artifact: NewArtifactViewerModel(),
		log:      NewLogViewerModel(),
	}
}

// SetSize updates the dashboard dimensions and propagates to sub-components.
func (d *DashboardModel) SetSize(width, height int) {
	d.width = width
	d.height = height

	// Layout: left pane 25%, right pane 75%, bottom log 30% height
	leftW := max(20, width/4)
	rightW := max(20, width-leftW-1) // 1 for divider
	topH := max(3, height*70/100)
	logH := max(3, height-topH-1) // 1 for divider

	d.menu.SetSize(leftW, topH)
	d.artifact.SetSize(rightW, topH)
	d.log.SetSize(width, logH)
}

// SetAgents updates the menu with the current agent list.
func (d *DashboardModel) SetAgents(agents []AgentRow) {
	d.menu.SetAgents(agents)
}

// Update routes messages through the FSM and delegates to the focused sub-model.
func (d DashboardModel) Update(msg tea.Msg) (DashboardModel, tea.Cmd) {
	d.PendingIntent = nil

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return d.handleKey(msg)
	case agentSelectedMsg:
		// Agent selection changed — update artifact viewer
		d.artifact.SetAgent(msg.id)
		return d, nil
	}

	// Delegate non-key messages to focused sub-model
	switch d.focus {
	case FocusMenu:
		var cmd tea.Cmd
		d.menu, cmd = d.menu.Update(msg)
		return d, cmd
	case FocusArtTop, FocusArtBottom:
		var cmd tea.Cmd
		d.artifact, cmd = d.artifact.Update(msg)
		return d, cmd
	case FocusLog:
		var cmd tea.Cmd
		d.log, cmd = d.log.Update(msg)
		return d, cmd
	}
	return d, nil
}

func (d DashboardModel) handleKey(msg tea.KeyPressMsg) (DashboardModel, tea.Cmd) {
	switch msg.Code {
	case tea.KeyTab:
		if msg.Mod.Contains(tea.ModShift) {
			return d.focusPrev(), nil
		}
		return d.focusNext(), nil
	case tea.KeyEscape:
		if d.focus == FocusMenu {
			d.PendingIntent = CloseDashboardIntent{}
			return d, nil
		}
		d.focus = FocusMenu
		d.menu.focused = true
		d.artifact.focusTop = false
		d.log.focused = false
		return d, nil
	}

	// Delegate to focused sub-model
	switch d.focus {
	case FocusMenu:
		var cmd tea.Cmd
		d.menu, cmd = d.menu.Update(msg)
		// Check if menu emitted an agent selection
		if d.menu.PendingIntent != nil {
			if sel, ok := d.menu.PendingIntent.(agentSelectedMsg); ok {
				d.artifact.SetAgent(sel.id)
				d.menu.PendingIntent = nil
			}
		}
		// Enter from menu focuses artifact top
		if msg.Code == tea.KeyEnter {
			d.focus = FocusArtTop
			d.menu.focused = false
			d.artifact.focusTop = true
		}
		return d, cmd
	case FocusArtTop:
		var cmd tea.Cmd
		d.artifact, cmd = d.artifact.Update(msg)
		return d, cmd
	case FocusArtBottom:
		var cmd tea.Cmd
		d.artifact, cmd = d.artifact.Update(msg)
		return d, cmd
	case FocusLog:
		var cmd tea.Cmd
		d.log, cmd = d.log.Update(msg)
		return d, cmd
	}
	return d, nil
}

func (d DashboardModel) focusNext() DashboardModel {
	switch d.focus {
	case FocusMenu:
		d.focus = FocusArtTop
		d.menu.focused = false
		d.artifact.focusTop = true
	case FocusArtTop:
		d.focus = FocusArtBottom
		d.artifact.focusTop = false
	case FocusArtBottom:
		d.focus = FocusLog
		d.log.focused = true
	case FocusLog:
		d.focus = FocusMenu
		d.log.focused = false
		d.menu.focused = true
	}
	return d
}

func (d DashboardModel) focusPrev() DashboardModel {
	switch d.focus {
	case FocusMenu:
		d.focus = FocusLog
		d.menu.focused = false
		d.log.focused = true
	case FocusArtTop:
		d.focus = FocusMenu
		d.artifact.focusTop = false
		d.menu.focused = true
	case FocusArtBottom:
		d.focus = FocusArtTop
		d.artifact.focusTop = true
	case FocusLog:
		d.focus = FocusArtBottom
		d.log.focused = false
	}
	return d
}

// View renders the full 3-pane dashboard layout.
func (d DashboardModel) View() string {
	if d.width < minWidth || d.height < 5 {
		return " Dashboard: terminal too small"
	}

	leftW := max(20, d.width/4)
	rightW := max(20, d.width-leftW-1)
	topH := max(3, d.height*70/100)

	// Top row: menu | artifacts
	menuView := d.menu.View()
	artView := d.artifact.View()

	// Pad/truncate to fit
	menuLines := strings.Split(menuView, "\n")
	artLines := strings.Split(artView, "\n")

	var topRows []string
	for i := 0; i < topH; i++ {
		left := ""
		if i < len(menuLines) {
			left = menuLines[i]
		}
		right := ""
		if i < len(artLines) {
			right = artLines[i]
		}
		// Pad left to leftW
		left = padRight(left, leftW)
		right = padRight(right, rightW)
		topRows = append(topRows, left+"│"+right)
	}
	topSection := strings.Join(topRows, "\n")

	// Divider
	divider := strings.Repeat("─", d.width)

	// Bottom: log pane
	logView := d.log.View()

	return topSection + "\n" + divider + "\n" + logView
}

// truncateToWidth truncates a string to fit within maxWidth visible characters.
func truncateToWidth(s string, maxWidth int) string {
	if lipgloss.Width(s) <= maxWidth {
		return s
	}
	// Simple byte truncation (works for ASCII, approximate for wide chars)
	for len(s) > 0 && lipgloss.Width(s) > maxWidth {
		s = s[:len(s)-1]
	}
	return s
}

// --- Dashboard data helpers ---

// AgentCard holds display data for one agent in the menu.
type AgentCard struct {
	ID             string
	State          AgentState
	ModelDisplay   string
	InputTokens    int64
	OutputTokens   int64
	Elapsed        time.Duration
	ContextWindow  int64
	TokPerSec      float64
	IsLive         bool
	PlanHistoryDir string
}

// agentRowToCard converts an AgentRow to an AgentCard for the dashboard menu.
func agentRowToCard(row AgentRow) AgentCard {
	elapsed := row.Elapsed
	if elapsed == 0 && !row.StartedAt.IsZero() {
		elapsed = time.Since(row.StartedAt)
	}
	return AgentCard{
		ID:            row.ID,
		State:         row.State,
		ModelDisplay:  row.ModelDisplay,
		InputTokens:   row.InputTokens,
		OutputTokens:  row.OutputTokens,
		Elapsed:       elapsed,
		ContextWindow: row.ContextWindow,
		IsLive:        row.State == AgentStateRunning,
	}
}

// --- Unused viewport removal helper (to satisfy linter for removed field) ---
var _ = viewport.New // ensure import is used
var _ = fmt.Sprintf  // ensure import is used
