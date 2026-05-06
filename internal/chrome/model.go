//go:build darwin

package chrome

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Result is returned by Run when chrome exits.
type Result struct {
	// NewActive is the tab index to switch to, or -1 to keep current.
	NewActive int
	// Quit signals the mux should shut down.
	Quit bool
}

// Model is the BubbleTea model for the chrome overlay.
type Model struct {
	snapshot Snapshot
	cursor   int  // currently highlighted tab
	showLogs bool // whether the log section is visible
	width    int
	height   int
	result   Result
	done     bool
}

// NewModel creates a chrome overlay model from a snapshot.
func NewModel(snap Snapshot) Model {
	cursor := snap.ActiveTab
	if cursor < 0 || cursor >= len(snap.Tabs) {
		cursor = 0
	}
	return Model{
		snapshot: snap,
		cursor:   cursor,
		showLogs: true,
		width:    snap.Width,
		height:   snap.Height,
	}
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.result = Result{Quit: true}
			m.done = true
			return m, tea.Quit

		case "enter", "esc":
			// Resume on current tab (or the cursor selection).
			m.result = Result{NewActive: m.cursor}
			m.done = true
			return m, tea.Quit

		case "j", "down":
			if m.cursor < len(m.snapshot.Tabs)-1 {
				m.cursor++
			}
			return m, nil

		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil

		case "l":
			m.showLogs = !m.showLogs
			return m, nil

		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			idx := int(msg.String()[0] - '1')
			if idx >= 0 && idx < len(m.snapshot.Tabs) {
				m.result = Result{NewActive: idx}
				m.done = true
				return m, tea.Quit
			}
			return m, nil
		}
	}
	return m, nil
}

func (m Model) View() string {
	if m.width == 0 {
		return ""
	}

	var sections []string

	// Title + Pipeline header.
	sections = append(sections, m.renderHeader())

	// Tab list.
	sections = append(sections, m.renderTabs())

	// Logs (optional).
	if m.showLogs && len(m.snapshot.Logs) > 0 {
		sections = append(sections, m.renderLogs())
	}

	// Keybinding legend.
	sections = append(sections, m.renderLegend())

	content := lipgloss.JoinVertical(lipgloss.Left, sections...)

	// Wrap in a border.
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(primaryColor).
		Padding(1, 2).
		Width(m.width - 4).
		Render(content)

	return box
}

// GetResult returns the chrome result after the model exits.
func (m Model) GetResult() Result {
	return m.result
}

// --- render helpers ---

func (m Model) renderHeader() string {
	title := titleStyle.Render("Orqestra")

	// Pipeline progress bar.
	phases := []struct {
		phase PipelinePhase
		name  string
	}{
		{PhaseIntake, "Intake"},
		{PhasePlanner, "Plan"},
		{PhaseValidator, "Validate"},
		{PhaseWorker, "Workers"},
	}

	var pipeline []string
	for _, p := range phases {
		var marker string
		if p.phase < m.snapshot.Phase {
			marker = doneStyle.Render("✓ " + p.name)
		} else if p.phase == m.snapshot.Phase {
			marker = activeStyle.Render("● " + p.name)
		} else {
			marker = waitingStyle.Render("· " + p.name)
		}
		pipeline = append(pipeline, marker)
	}
	pipelineStr := strings.Join(pipeline, dimStyle.Render(" ─▶ "))

	goal := ""
	if m.snapshot.Goal != "" {
		goalText := m.snapshot.Goal
		if len(goalText) > m.width-20 {
			goalText = goalText[:m.width-23] + "..."
		}
		goal = "\n" + dimStyle.Render("Goal: ") + goalText
	}

	return title + "\n" + pipelineStr + goal
}

func (m Model) renderTabs() string {
	header := sectionStyle.Render("Sessions")
	var rows []string

	for i, tab := range m.snapshot.Tabs {
		prefix := "  "
		if i == m.cursor {
			prefix = "▸ "
		}

		// State indicator.
		var stateStr string
		switch tab.State {
		case TabStateRunning:
			elapsed := time.Since(tab.StartedAt).Truncate(time.Second)
			stateStr = activeStyle.Render(fmt.Sprintf("● running  (%s)", elapsed))
		case TabStateDone:
			if tab.ExitCode == 0 {
				stateStr = doneStyle.Render("✓ completed")
			} else {
				stateStr = errorStyle.Render(fmt.Sprintf("✗ failed (code %d)", tab.ExitCode))
			}
		}

		// Attention marker.
		attention := ""
		if tab.Attention {
			attention = attentionStyle.Render(" ⚠")
		}

		// Active marker.
		activeMarker := ""
		if i == m.snapshot.ActiveTab {
			activeMarker = dimStyle.Render(" ◄")
		}

		numStr := dimStyle.Render(fmt.Sprintf("[%d]", i+1))
		row := fmt.Sprintf("%s%s %-12s %s%s%s", prefix, numStr, tab.Name, stateStr, attention, activeMarker)
		rows = append(rows, row)
	}

	return header + "\n" + strings.Join(rows, "\n")
}

func (m Model) renderLogs() string {
	header := sectionStyle.Render("Recent Logs")

	// Show last 5 logs max.
	logs := m.snapshot.Logs
	if len(logs) > 5 {
		logs = logs[len(logs)-5:]
	}

	var rows []string
	for _, entry := range logs {
		ts := entry.Time.Format("15:04:05")
		var levelStr string
		switch entry.Level {
		case "ERROR":
			levelStr = errorStyle.Render(entry.Level)
		case "WARN":
			levelStr = attentionStyle.Render(entry.Level)
		default:
			levelStr = dimStyle.Render(entry.Level)
		}
		row := fmt.Sprintf("  %s [%s] %s", dimStyle.Render(ts), levelStr, entry.Message)
		rows = append(rows, row)
	}

	return header + "\n" + strings.Join(rows, "\n")
}

func (m Model) renderLegend() string {
	items := []string{
		"1-9: Switch Tab",
		"Enter: Resume",
		"l: Toggle Logs",
		"q: Quit",
	}
	return "\n" + dimStyle.Render(strings.Join(items, " │ "))
}
