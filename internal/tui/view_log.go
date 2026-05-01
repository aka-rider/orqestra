package tui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// LogEntry represents a single structured log entry.
type LogEntry struct {
	Time    time.Time
	Level   string
	Message string
	Attrs   map[string]string
}

// logPanel displays structured log entries as a scrollable table at the bottom.
type logPanel struct {
	entries  []LogEntry
	mu       sync.Mutex
	width    int
	height   int
	viewport viewport.Model
	ready    bool
	focused  bool
}

func newLogPanel() *logPanel {
	return &logPanel{
		height: 8,
	}
}

func (l *logPanel) Add(entry LogEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, entry)
	// Keep last 200 entries
	if len(l.entries) > 200 {
		l.entries = l.entries[len(l.entries)-200:]
	}
	l.refreshContent()
}

func (l *logPanel) SetWidth(w int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.width = w
	if l.width > 4 {
		l.initViewport()
		l.refreshContent()
	}
}

func (l *logPanel) SetHeight(h int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.height = h
	if l.width > 4 {
		l.initViewport()
		l.refreshContent()
	}
}

func (l *logPanel) SetFocused(focused bool) {
	l.focused = focused
}

func (l *logPanel) IsFocused() bool {
	return l.focused
}

func (l *logPanel) Update(msg tea.Msg) tea.Cmd {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.ready {
		return nil
	}
	var cmd tea.Cmd
	l.viewport, cmd = l.viewport.Update(msg)
	return cmd
}

func (l *logPanel) initViewport() {
	contentHeight := l.height - 3 // header + sep + border
	if contentHeight < 1 {
		contentHeight = 1
	}
	w := l.width - 4
	if w < 10 {
		w = 10
	}
	l.viewport = viewport.New(w, contentHeight)
	l.ready = true
}

func (l *logPanel) refreshContent() {
	if !l.ready {
		return
	}
	var rows []string
	for _, e := range l.entries {
		ts := e.Time.Format("15:04:05")
		lvl := e.Level

		var lvlStyled string
		switch lvl {
		case "INFO":
			lvlStyled = logInfoStyle.Render(lvl)
		case "ERROR":
			lvlStyled = logErrorStyle.Render(lvl)
		case "WARN":
			lvlStyled = logWarnStyle.Render(lvl)
		default:
			lvlStyled = lvl
		}

		var attrs []string
		for k, v := range e.Attrs {
			attrs = append(attrs, fmt.Sprintf("%s=%s", k, v))
		}
		detail := strings.Join(attrs, " ")

		msg := e.Message
		if len(msg) > 20 {
			msg = msg[:17] + "..."
		}

		maxDetail := l.width - 48
		if maxDetail < 0 {
			maxDetail = 0
		}
		if len(detail) > maxDetail && maxDetail > 3 {
			detail = detail[:maxDetail-3] + "..."
		}

		row := fmt.Sprintf(" %-8s │ %-5s │ %-20s │ %s", ts, lvlStyled, msg, detail)
		rows = append(rows, row)
	}

	l.viewport.SetContent(strings.Join(rows, "\n"))
	l.viewport.GotoBottom()
}

func (l *logPanel) View() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.width < 20 || !l.ready {
		return ""
	}

	// Table header
	header := logTableHeaderStyle.Render(
		fmt.Sprintf(" %-8s │ %-5s │ %-20s │ %s", "TIME", "LEVEL", "MESSAGE", "DETAILS"),
	)
	sep := logTableSepStyle.Render(strings.Repeat("─", l.width-4))

	var borderColor lipgloss.Color
	if l.focused {
		borderColor = lipgloss.Color("#7C3AED")
	} else {
		borderColor = lipgloss.Color("#4B5563")
	}

	content := header + "\n" + sep + "\n" + l.viewport.View()

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder(), true, false, false, false).
		BorderForeground(borderColor).
		Width(l.width - 2).
		Render(content)
}

// logTableHeaderStyle styles the table header.
var logTableHeaderStyle = lipgloss.NewStyle().
	Bold(true).
	Foreground(lipgloss.Color("#9CA3AF"))

var logTableSepStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("#4B5563"))

var logInfoStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("#60A5FA"))

var logErrorStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("#EF4444")).
	Bold(true)

var logWarnStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("#FBBF24"))
