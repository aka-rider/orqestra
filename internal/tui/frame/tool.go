package frame

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"
)

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

// ToolStyles carries the four status styles (one field of the frame palette).
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
	toolName string
	detail   string
	status   ToolStatus
	rows     []Row
}

// NewTool creates a pending tool frame. It owns its status styles via the palette.
func NewTool(toolName, detail string) Tool {
	return Tool{toolName: toolName, detail: detail, status: ToolPending}
}

// Status reports the current lifecycle state.
func (t Tool) Status() ToolStatus { return t.status }

// CollapseGroup marks resolved tool frames as foldable activity (frame.Collapsible).
func (t Tool) CollapseGroup() string { return "tool" }

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
	icon := t.statusIcon()
	toolIcon := t.toolIcon()
	st := t.statusStyle()
	var spans []Span
	spans = append(spans, Span{Text: icon, Style: st})
	spans = append(spans, Span{Text: " ", Style: st}) // gap between status and tool group
	spans = append(spans, Span{Text: toolIcon, Style: st})
	spans = append(spans, Span{Text: " ", Style: st}) // gap between tool icon and name
	spans = append(spans, Span{Text: t.toolName, Style: st})
	if t.detail != "" {
		// Use runewidth.StringWidth for display-column-accurate prefix length.
		// Status icon (e.g. "✓ ") = width 2, gap = 1, tool icon = 1–2, gap = 1, toolName = variable.
		prefixW := runewidth.StringWidth(icon) + 1 +
			runewidth.StringWidth(toolIcon) + 1 + runewidth.StringWidth(t.toolName)
		detailW := w - prefixW - 2 // 2 for "()"
		if detailW > 0 {
			truncated := truncateToTail(t.detail, detailW)
			spans = append(spans, Span{Text: "(", Style: st})
			spans = append(spans, Span{Text: truncated, Style: st})
			spans = append(spans, Span{Text: ")", Style: st})
		}
	}
	return []Row{{Cells: cellsFromSpans(spans)}}
}

func (t Tool) toolIcon() string {
	switch t.toolName {
	case "Read", "TodoRead":
		return "✑"
	case "Write", "MultiEdit", "TodoWrite":
		return "✎"
	case "Bash":
		return "❯"
	case "Grep", "Glob":
		return "⚲"
	default:
		if strings.HasPrefix(t.toolName, "mcp__") {
			return "⚒"
		}
		return "·"
	}
}

func (t Tool) statusIcon() string {
	switch t.status {
	case ToolOK:
		return "✓ "
	case ToolErr:
		return "✗ "
	case ToolUnknown:
		return "· "
	default:
		return "◌ "
	}
}

func (t Tool) statusStyle() lipgloss.Style {
	switch t.status {
	case ToolOK:
		return theme.Tool.OK
	case ToolErr:
		return theme.Tool.Err
	case ToolUnknown:
		return theme.Tool.Unknown
	default:
		return theme.Tool.Pending
	}
}
