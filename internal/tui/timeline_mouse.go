package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/xiii/orqestra/internal/tui/frame"
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
			if t.anchor != t.cursor {
				// Drag selection: mark it (explicit copy only, no auto-copy).
				t.hasSel = true
			} else {
				// Click (no drag): copy the whole frame under the cursor.
				t.hasSel = false
				return t, t.CopyFrame(t.screenRowIdx(m.X, m.Y))
			}
		}
	}
	return t, nil
}

// handleAutoscrollTick drives continuous autoscroll while a drag holds outside rect.
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

// screenToPos converts terminal coordinates to a visual selection position.
func (t Timeline) screenToPos(x, y int) selPos {
	if len(t.rows) == 0 {
		return selPos{}
	}
	ri := t.screenRowIdx(x, y)
	localX := max(0, x-t.rect.Min.X)
	return selPos{row: ri, col: visualToRuneCol(t.rows[ri].cells, localX)}
}

// screenRowIdx converts screen Y to a flat row index.
func (t Timeline) screenRowIdx(_, y int) int {
	return clamp(t.top+(y-t.rect.Min.Y), 0, max(0, len(t.rows)-1))
}

// visualToRuneCol maps a visual column offset to a rune-column index in cells.
func visualToRuneCol(cells []frame.Cell, visX int) int {
	x := 0
	for i, c := range cells {
		if x >= visX {
			return i
		}
		x += c.W
	}
	return len(cells)
}
