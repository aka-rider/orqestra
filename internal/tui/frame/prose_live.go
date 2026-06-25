package frame

import (
	"strings"

	"charm.land/lipgloss/v2"
	tea "charm.land/bubbletea/v2"
)

// BlinkMsg toggles a live frame's cursor. The Timeline owns the blink tick
// lifecycle and forwards this to its tail; live frames that have no cursor
// (e.g. a running tool) ignore it.
type BlinkMsg struct{}

// LiveProse is the in-progress assistant prose: the streaming text plus a
// blinking cursor. It is the InteractiveFrame tail while the model is talking;
// it accumulates deltas and Resolve()s into a static prose frame when the
// turn's text completes. Live lines are truncated (not wrapped) to match the
// streaming look; the resolved prose frame wraps.
type LiveProse struct {
	text    string
	style   lipgloss.Style
	width   int
	blinkOn bool
	rows    []Row
}

// NewLiveProse starts an empty live-prose tail rendered in the given style.
func NewLiveProse(style lipgloss.Style) LiveProse {
	p := LiveProse{style: style}
	p.rows = p.layout(p.width)
	return p
}

// Append accumulates a streaming delta.
func (p LiveProse) Append(delta string) LiveProse {
	p.text += delta
	p.rows = p.layout(p.width)
	return p
}

func (p LiveProse) SetWidth(w int) InteractiveFrame {
	p.width = w
	p.rows = p.layout(w)
	return p
}

func (p LiveProse) Update(msg tea.Msg) (InteractiveFrame, tea.Cmd) {
	if _, ok := msg.(BlinkMsg); ok {
		p.blinkOn = !p.blinkOn
		p.rows = p.layout(p.width)
	}
	return p, nil
}

func (p LiveProse) Rows() []Row { return p.rows }

// Resolve promotes the accumulated text to a static, wrapped prose frame.
func (p LiveProse) Resolve() StaticFrame { return NewProse(p.text) }

func (p LiveProse) layout(w int) []Row {
	lines := strings.Split(p.text, "\n")
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var rows []Row
	for _, line := range lines {
		s := " " + truncate(line, max(0, w-2))
		rows = append(rows, Row{Cells: cellsFromSpans([]Span{{Text: s, Style: p.style}})})
	}
	cursor := p.style
	if p.blinkOn {
		cursor = cursor.Faint(true)
	}
	rows = append(rows, Row{Cells: cellsFromSpans([]Span{{Text: "⏺", Style: cursor}})})
	return rows
}
