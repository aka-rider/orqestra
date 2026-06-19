package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// streamLineKind classifies visible lines in the streaming console.
type streamLineKind int

const (
	streamLinePending  streamLineKind = iota // tool use awaiting result
	streamLineToolOK                         // resolved tool use (success)
	streamLineToolErr                        // resolved tool use (error)
)

// streamConsoleLine is one tool-activity line in the streaming console.
type streamConsoleLine struct {
	kind streamLineKind
	text string // "tool detail" display text
}

// streamingConsole is the borderless live-output region shown below the
// transcript while an agent is active. It shows pending/resolved tool lines
// and the current in-progress partial speech text with a slow blink.
//
// Value sub-model: callers hold copies; Update returns new copies + cmd.
type streamingConsole struct {
	speechPartial string             // current in-progress text (replaced, not appended per agent line)
	toolLines     []streamConsoleLine // pending and resolved tool lines
	blinkOn       bool
	blinkTag      int
	active        bool
	width         int
}

const streamBlinkInterval = 500 * time.Millisecond

// streamBlinkMsg is the tagged self-tick for the in-progress indicator.
type streamBlinkMsg struct{ tag int }

// newStreamingConsole returns a zero-value console ready for use.
func newStreamingConsole(width int) streamingConsole {
	return streamingConsole{width: width, active: false}
}

// Start marks the console active and begins the blink loop.
func (c streamingConsole) Start() (streamingConsole, tea.Cmd) {
	c.active = true
	c.blinkTag++
	return c, c.blinkCmd()
}

// Reset returns the console to its zero state.
func (c streamingConsole) Reset() streamingConsole {
	c.speechPartial = ""
	c.toolLines = nil
	c.blinkOn = false
	c.active = false
	return c
}

// ClearForAgent clears tool lines and the speech partial for an agent
// transition, keeping the blink loop alive (tag and active unchanged).
func (c streamingConsole) ClearForAgent() streamingConsole {
	c.speechPartial = ""
	c.toolLines = nil
	return c
}

// RenderFixed renders the console as exactly h rows, padding with blank lines
// or truncating when DesiredHeight() differs. Each row ends with '\n'.
func (c streamingConsole) RenderFixed(h, w int) string {
	if h <= 0 {
		return ""
	}
	c.width = w
	raw := c.View()
	if raw == "" {
		return strings.Repeat("\n", h)
	}
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	var b strings.Builder
	for i := range h {
		if i < len(lines) {
			b.WriteString(lines[i])
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// AppendDelta accumulates a streaming text delta into the partial.
func (c streamingConsole) AppendDelta(text string) streamingConsole {
	c.speechPartial += text
	return c
}

// CompletePartial clears the partial (the completed line was promoted to transcript).
func (c streamingConsole) CompletePartial() streamingConsole {
	c.speechPartial = ""
	return c
}

// AddPendingTool adds a tool-use line in the pending state.
func (c streamingConsole) AddPendingTool(text string) streamingConsole {
	c.toolLines = append(c.toolLines, streamConsoleLine{kind: streamLinePending, text: text})
	return c
}

// ResolveLastTool resolves the most recent pending tool line to ok or error.
func (c streamingConsole) ResolveLastTool(isErr bool) streamingConsole {
	for i := len(c.toolLines) - 1; i >= 0; i-- {
		if c.toolLines[i].kind == streamLinePending {
			if isErr {
				c.toolLines[i].kind = streamLineToolErr
			} else {
				c.toolLines[i].kind = streamLineToolOK
			}
			return c
		}
	}
	return c
}

// SetWidth updates the console width (called from recalculateLayout).
func (c streamingConsole) SetWidth(w int) streamingConsole {
	c.width = w
	return c
}

// DesiredHeight returns how many terminal rows the console wants to occupy.
// Returns 0 when inactive.
func (c streamingConsole) DesiredHeight() int {
	if !c.active {
		return 0
	}
	lines := len(c.toolLines)
	if c.speechPartial != "" || c.active {
		lines++ // indicator line
	}
	return lines
}

// Update handles blink ticks.
func (c streamingConsole) Update(msg tea.Msg) (streamingConsole, tea.Cmd) {
	if m, ok := msg.(streamBlinkMsg); ok {
		if !c.active || m.tag != c.blinkTag {
			return c, nil
		}
		c.blinkOn = !c.blinkOn
		return c, c.blinkCmd()
	}
	return c, nil
}

// View renders the console as a borderless region. Pure.
func (c streamingConsole) View() string {
	if !c.active && len(c.toolLines) == 0 {
		return ""
	}

	w := c.width
	if w <= 0 {
		w = 80
	}

	var b strings.Builder

	for _, line := range c.toolLines {
		var s string
		switch line.kind {
		case streamLinePending:
			s = streamToolPendingStyle.Render("◌ " + truncate(line.text, w-2))
		case streamLineToolOK:
			s = streamToolOKStyle.Render("✓ " + truncate(line.text, w-2))
		case streamLineToolErr:
			s = streamToolErrStyle.Render("✗ " + truncate(line.text, w-2))
		}
		b.WriteString(s)
		b.WriteByte('\n')
	}

	// Indicator line: steady speech partial + blinking ⏺ cursor.
	if c.active {
		text := streamSpeechStyle.Render(truncate(c.speechPartial, max(0, w-2)))
		cursor := streamSpeechStyle.Faint(c.blinkOn).Render("⏺")
		b.WriteString(text + cursor)
	}

	return b.String()
}

// blinkCmd returns the next tagged blink tick.
func (c streamingConsole) blinkCmd() tea.Cmd {
	tag := c.blinkTag
	return tea.Tick(streamBlinkInterval, func(time.Time) tea.Msg {
		return streamBlinkMsg{tag: tag}
	})
}

// truncate clips s to maxLen runes.
func truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen])
}

// Stream style vars (borderless; replaces streamBlockStyle which is retired).
var (
	streamSpeechStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("15")) // bright white

	streamToolOKStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("2")) // green

	streamToolErrStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("3")) // yellow

	streamToolPendingStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("244")) // dim grey
)
