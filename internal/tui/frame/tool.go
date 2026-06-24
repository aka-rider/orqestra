package frame

import "charm.land/lipgloss/v2"

// ToolStatus is the lifecycle state of a tool invocation. A Tool frame starts
// Pending and is resolved to OK/Err when its result arrives, or to Unknown if
// the agent completes with the result still outstanding.
type ToolStatus uint8

const (
	ToolPending ToolStatus = iota
	ToolOK
	ToolErr
	ToolUnknown
)

// ToolStyles carries the four status styles so the frame package stays free of
// any global style state; the Timeline supplies them at construction.
type ToolStyles struct {
	Pending lipgloss.Style
	OK      lipgloss.Style
	Err     lipgloss.Style
	Unknown lipgloss.Style
}

// Tool is a single-line tool-activity frame: a status icon plus a truncated
// description. It never wraps. Its status is mutable in place (Pending →
// OK/Err/Unknown) — a legal state of one frame type, not a kind tag.
type Tool struct {
	text   string
	status ToolStatus
	sty    ToolStyles
	rows   []Row
}

// NewTool creates a pending tool frame.
func NewTool(text string, sty ToolStyles) Tool {
	return Tool{text: text, status: ToolPending, sty: sty}
}

// Status reports the current lifecycle state.
func (t Tool) Status() ToolStatus { return t.status }

// WithStatus returns a copy in the given status, re-laid-out at the prior width.
func (t Tool) WithStatus(s ToolStatus) Tool {
	t.status = s
	t.rows = t.layout(t.rowWidth())
	return t
}

func (t Tool) SetWidth(w int) StaticFrame {
	t.rows = t.layout(w)
	return t
}

func (t Tool) Rows() []Row { return t.rows }

// rowWidth recovers the width the frame was last laid out at (icon + text).
func (t Tool) rowWidth() int {
	if len(t.rows) == 0 {
		return 0
	}
	return t.rows[0].Width()
}

func (t Tool) layout(w int) []Row {
	icon, style := t.iconStyle()
	s := icon + truncate(t.text, max(0, w-2))
	return []Row{{Cells: cellsFromSpans([]Span{{Text: s, Style: style}})}}
}

func (t Tool) iconStyle() (string, lipgloss.Style) {
	switch t.status {
	case ToolOK:
		return "✓ ", t.sty.OK
	case ToolErr:
		return "✗ ", t.sty.Err
	case ToolUnknown:
		return "· ", t.sty.Unknown
	default:
		return "◌ ", t.sty.Pending
	}
}
