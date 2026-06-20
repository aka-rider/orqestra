package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

const timelineAutoscrollDelay = 40 * time.Millisecond

// handleMouse processes mouse events routed to the timeline.
func (t Timeline) handleMouse(msg tea.MouseMsg) (Timeline, tea.Cmd) {
	switch m := msg.(type) {
	case tea.MouseWheelMsg:
		switch m.Button {
		case tea.MouseWheelUp:
			return t.handleWheel(-3), nil
		case tea.MouseWheelDown:
			return t.handleWheel(3), nil
		}

	case tea.MouseClickMsg:
		if m.Button == tea.MouseLeft {
			t.follow = false
			t.hasSel = false
			t.selecting = true
			t.anchor = t.screenToPos(m.X, m.Y)
			t.cursor = t.anchor
			t.dragSeq++
		}

	case tea.MouseMotionMsg:
		if t.selecting && m.Button == tea.MouseLeft {
			t.cursor = t.screenToPos(m.X, m.Y)
			if m.Y < t.rect.Min.Y {
				t.autoscrollDir = -1
				return t, t.autoscrollCmd()
			} else if m.Y >= t.rect.Max.Y {
				t.autoscrollDir = 1
				return t, t.autoscrollCmd()
			}
			t.autoscrollDir = 0
			return t, nil
		}

	case tea.MouseReleaseMsg:
		if m.Button == tea.MouseLeft && t.selecting {
			t.selecting = false
			t.autoscrollDir = 0
			t.dragSeq++
			if !timelinePosEqual(t.anchor, t.cursor) {
				// Drag selection: set hasSel (no auto-copy; explicit copy only).
				t.hasSel = true
			} else {
				// Click (no drag): copy the whole frame.
				t.hasSel = false
				rowIdx := t.screenRowIdx(m.X, m.Y)
				return t, t.CopyFrame(rowIdx)
			}
		}
	}
	return t, nil
}

// handleAutoscrollTick drives continuous autoscroll while button held outside rect.
func (t Timeline) handleAutoscrollTick(msg timelineAutoscrollMsg) (Timeline, tea.Cmd) {
	if msg.seq != t.dragSeq || !t.selecting || t.autoscrollDir == 0 {
		return t, nil
	}
	t.top += t.autoscrollDir
	t.clampTop()
	return t, t.autoscrollCmd()
}

func (t Timeline) autoscrollCmd() tea.Cmd {
	seq := t.dragSeq
	return tea.Tick(timelineAutoscrollDelay, func(time.Time) tea.Msg {
		return timelineAutoscrollMsg{seq: seq}
	})
}

// handleWheel scrolls the timeline by delta rows.
func (t Timeline) handleWheel(delta int) Timeline {
	t.top += delta
	t.clampTop()
	t.follow = t.AtBottom()
	if t.follow {
		t.scrollToBottom()
	}
	return t
}

// screenToPos converts terminal coordinates to a logical position.
// For opaque rows: clamp to line boundaries (col 0 or line length) for
// line-granular selection.
func (t Timeline) screenToPos(screenX, screenY int) timelinePos {
	if len(t.rows) == 0 {
		return timelinePos{}
	}
	rowIdx := t.screenRowIdx(screenX, screenY)
	row := t.rows[rowIdx]

	if row.opaque {
		// Line-granular: snap to line start or end.
		mid := t.rect.Min.X + t.rect.Dx()/2
		if screenX < mid {
			return timelinePos{line: row.lineIdx, col: 0}
		}
		return timelinePos{line: row.lineIdx, col: len(row.cells)}
	}

	localX := screenX - t.rect.Min.X
	if localX < 1 {
		localX = 0
	} else {
		localX-- // account for 1-char left margin
	}
	runeCol := row.startCol + visualToTimelineRuneCol(row.cells, localX)
	return timelinePos{line: row.lineIdx, col: runeCol}
}

// screenRowIdx converts screen Y to a row index in t.rows.
func (t Timeline) screenRowIdx(_, screenY int) int {
	localY := screenY - t.rect.Min.Y
	rowIdx := t.top + localY
	if rowIdx < 0 {
		rowIdx = 0
	}
	if rowIdx >= len(t.rows) {
		rowIdx = len(t.rows) - 1
	}
	if rowIdx < 0 {
		rowIdx = 0
	}
	return rowIdx
}

// visualToTimelineRuneCol maps a visual offset to a rune-column index.
func visualToTimelineRuneCol(cells []timelineCell, visX int) int {
	x := 0
	for i, c := range cells {
		if x >= visX {
			return i
		}
		x += c.w
	}
	return len(cells)
}
