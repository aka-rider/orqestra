package frame

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Phase is a full-width separator rule, optionally labelled:
// "────[ label ]────────". Unlike other frames it spans the whole width with
// no gutter, marking an agent/phase transition in the transcript.
type Phase struct {
	label string
	rows  []Row
}

// NewPhase creates a phase-separator rule with the given label. It owns its rule
// style via the frame palette.
func NewPhase(label string) StaticFrame {
	return Phase{label: label}
}

func (p Phase) SetWidth(w int) StaticFrame {
	p.rows = []Row{ruleRow(p.label, w, theme.Phase)}
	return p
}

func (p Phase) Rows() []Row { return p.rows }

// ruleRow builds a single rule row at width w.
func ruleRow(label string, w int, style lipgloss.Style) Row {
	if w <= 0 {
		return Row{}
	}
	var s string
	if label == "" {
		s = strings.Repeat("─", w)
	} else {
		left := "────[ " + label + " ]"
		fill := max(0, w-lipgloss.Width(left))
		s = left + strings.Repeat("─", fill)
	}
	return Row{Cells: cellsFromSpans([]Span{{Text: s, Style: style}})}
}
