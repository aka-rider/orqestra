package frame

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
)

// ConstToolGroupMax is the number of recent tool rows shown before the
// collapsed "… and +N more tools" summary. Exported for the footer hint.
const ConstToolGroupMax = 8

type turnTool struct {
	toolName string
	detail   string
	status   ToolStatus
}

// TurnGroup is the live InteractiveFrame tail for one agent turn. It groups the
// turn's streaming prose (shown as a brief ⏺ header) and its tool calls (the
// last ConstToolGroupMax, expandable). Pointer receivers throughout — the
// PipelineScreen's currentTurn and the Timeline's tail hold the same pointer, so
// mutations via either path are always visible.
type TurnGroup struct {
	prose    []string   // finalized prose lines
	partial  string     // streaming partial (from DeltaMsg)
	tools    []turnTool // ordered tool list for this turn
	expanded bool       // true = show all tools
	active   bool       // false after Seal()
	blinkOn  bool
	width    int
	rows     []Row
}

// NewTurnGroup returns an active, empty TurnGroup ready to accept streaming.
func NewTurnGroup() *TurnGroup {
	tg := &TurnGroup{active: true}
	tg.rows = tg.layout(0)
	return tg
}

// ToolCount returns the total number of tool calls accumulated this turn.
func (tg *TurnGroup) ToolCount() int { return len(tg.tools) }

// AddTool appends a pending tool entry and returns its local index for later
// resolution via ResolveTool.
func (tg *TurnGroup) AddTool(toolName, detail string) int {
	idx := len(tg.tools)
	tg.tools = append(tg.tools, turnTool{toolName: toolName, detail: detail, status: ToolPending})
	tg.rows = tg.layout(tg.width)
	return idx
}

// ResolveTool updates the status of the tool at the given local index.
func (tg *TurnGroup) ResolveTool(idx int, status ToolStatus) {
	if idx < 0 || idx >= len(tg.tools) {
		return
	}
	tg.tools[idx].status = status
	tg.rows = tg.layout(tg.width)
}

// FinalizeProse adds a completed prose line and clears the streaming partial.
func (tg *TurnGroup) FinalizeProse(line string) {
	if line != "" {
		tg.prose = append(tg.prose, line)
	}
	tg.partial = ""
	tg.rows = tg.layout(tg.width)
}

// SetExpanded toggles collapse/expand mode. No-op after Seal().
func (tg *TurnGroup) SetExpanded(v bool) {
	if !tg.active {
		return
	}
	tg.expanded = v
	tg.rows = tg.layout(tg.width)
}

// Seal marks the turn complete and auto-expands all tools. Must be called before
// Resolve() so the TurnSnapshot shows the full tool list.
func (tg *TurnGroup) Seal() {
	tg.active = false
	tg.expanded = true
	tg.rows = tg.layout(tg.width)
}

// --- InteractiveFrame ---

func (tg *TurnGroup) SetWidth(w int) InteractiveFrame {
	tg.width = w
	tg.rows = tg.layout(w)
	return tg
}

func (tg *TurnGroup) Update(msg tea.Msg) (InteractiveFrame, tea.Cmd) {
	switch m := msg.(type) {
	case DeltaMsg:
		tg.partial += m.Text
		tg.rows = tg.layout(tg.width)
	case BlinkMsg:
		tg.blinkOn = !tg.blinkOn
		tg.rows = tg.layout(tg.width)
	}
	return tg, nil
}

func (tg *TurnGroup) Rows() []Row { return tg.rows }

func (tg *TurnGroup) Resolve() StaticFrame { return newTurnSnapshot(tg) }

// layout builds the display rows for the current state:
//
//	Active:   " ⏺ <brief>"  (blinks; brief = partial or last prose line)
//	          "  … and +N more tools"  (dim; only when collapsed and >ConstToolGroupMax)
//	          " ◌  📖 Read(…)"         (last ConstToolGroupMax rows)
//
//	Sealed:   tool rows only, all shown (expanded=true after Seal).
func (tg *TurnGroup) layout(w int) []Row {
	var rows []Row

	if tg.active {
		brief := tg.partial
		if brief == "" && len(tg.prose) > 0 {
			brief = tg.prose[len(tg.prose)-1]
		}
		const pfxCols = 3 // " ⏺ "
		briefTrunc := truncate(brief, max(0, w-pfxCols))
		// Match LiveProse: cursor bright when blinkOn=false, faint when blinkOn=true.
		headStyle := theme.Live
		if tg.blinkOn {
			headStyle = headStyle.Faint(true)
		}
		headCells := cellsFromSpans([]Span{{Text: " ⏺ ", Style: headStyle}})
		if briefTrunc != "" {
			headCells = append(headCells, cellsFromSpans([]Span{{Text: briefTrunc, Style: theme.Prose}})...)
		}
		rows = append(rows, Row{Cells: headCells})
	}

	n := len(tg.tools)
	if n == 0 {
		return rows
	}
	start := 0
	if !tg.expanded && n > ConstToolGroupMax {
		extra := n - ConstToolGroupMax
		summary := fmt.Sprintf("  … and +%d more tools", extra)
		rows = append(rows, Row{Cells: cellsFromSpans([]Span{{Text: summary, Style: theme.Collapsed}})})
		start = extra
	}
	for i := start; i < n; i++ {
		tt := tg.tools[i]
		t := Tool{toolName: tt.toolName, detail: tt.detail, status: tt.status}
		rows = append(rows, t.layout(w)...)
	}
	return rows
}

// --- TurnSnapshot ---

// TurnSnapshot is the resolved static form of a completed TurnGroup. It shows all
// finalized prose lines followed by all tool calls, fully expanded. If both are
// empty Rows() returns nil so the Timeline skips appending a blank frame.
type TurnSnapshot struct {
	prose []string
	tools []turnTool
	rows  []Row
}

func newTurnSnapshot(tg *TurnGroup) TurnSnapshot {
	ts := TurnSnapshot{prose: tg.prose, tools: tg.tools}
	ts.rows = ts.layoutRows(tg.width)
	return ts
}

func (ts TurnSnapshot) SetWidth(w int) StaticFrame {
	ts.rows = ts.layoutRows(w)
	return ts
}

func (ts TurnSnapshot) Rows() []Row { return ts.rows }

func (ts TurnSnapshot) layoutRows(w int) []Row {
	if len(ts.prose) == 0 && len(ts.tools) == 0 {
		return nil
	}
	var rows []Row
	for _, line := range ts.prose {
		rows = append(rows, NewProse(line).SetWidth(w).Rows()...)
	}
	for _, tt := range ts.tools {
		t := Tool{toolName: tt.toolName, detail: tt.detail, status: tt.status}
		rows = append(rows, t.layout(w)...)
	}
	return rows
}
