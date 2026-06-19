package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

const transcriptAutoscrollDelay = 40 * time.Millisecond

// handleMouse processes mouse events routed to the transcript.
// tea.MouseMsg is an interface; concrete types are MouseClickMsg, MouseReleaseMsg,
// MouseWheelMsg, MouseMotionMsg.
func (t Transcript) handleMouse(msg tea.MouseMsg) (Transcript, tea.Cmd) {
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
			cmd := t.emitCopy()
			if m.Y < t.rect.Min.Y {
				t.autoscrollDir = -1
				return t, tea.Batch(cmd, t.autoscrollCmd())
			} else if m.Y >= t.rect.Max.Y {
				t.autoscrollDir = 1
				return t, tea.Batch(cmd, t.autoscrollCmd())
			}
			t.autoscrollDir = 0
			return t, cmd
		}

	case tea.MouseReleaseMsg:
		if m.Button == tea.MouseLeft && t.selecting {
			t.selecting = false
			t.autoscrollDir = 0
			t.dragSeq++ // invalidate in-flight autoscroll ticks
			if !posEqual(t.anchor, t.cursor) {
				t.hasSel = true
			} else {
				t.hasSel = false
			}
			return t, t.emitCopy()
		}
	}
	return t, nil
}

// handleAutoscrollTick drives continuous autoscroll while the button is held
// outside the transcript rect. Stale ticks (seq mismatch) are ignored.
func (t Transcript) handleAutoscrollTick(msg transcriptAutoscrollMsg) (Transcript, tea.Cmd) {
	if msg.seq != t.dragSeq || !t.selecting || t.autoscrollDir == 0 {
		return t, nil
	}
	t.top += t.autoscrollDir
	t.clampTop()
	return t, t.autoscrollCmd()
}

// autoscrollCmd returns a self-perpetuating tick tagged with the current dragSeq.
func (t Transcript) autoscrollCmd() tea.Cmd {
	seq := t.dragSeq
	return tea.Tick(transcriptAutoscrollDelay, func(time.Time) tea.Msg {
		return transcriptAutoscrollMsg{seq: seq}
	})
}

// handleWheel scrolls the transcript by delta rows and disables auto-follow
// when scrolling up.
func (t Transcript) handleWheel(delta int) Transcript {
	t.top += delta
	t.clampTop()
	t.follow = t.AtBottom()
	if t.follow {
		t.scrollToBottom()
	}
	return t
}

// screenToPos converts terminal coordinates to a logical Pos within the
// transcript, clamped to valid content.
func (t Transcript) screenToPos(screenX, screenY int) transcriptPos {
	if len(t.rows) == 0 {
		return transcriptPos{}
	}
	localY := screenY - t.rect.Min.Y
	localX := screenX - t.rect.Min.X

	rowIdx := t.top + localY
	if rowIdx < 0 {
		rowIdx = 0
	}
	if rowIdx >= len(t.rows) {
		rowIdx = len(t.rows) - 1
	}

	row := t.rows[rowIdx]
	runeCol := row.startCol + visualToRuneCol(row.cells, localX)
	return transcriptPos{line: row.lineIdx, col: runeCol}
}

// visualToRuneCol maps a visual (terminal-column) offset to a rune-column
// index within a cell slice. Wide runes occupy 2 visual columns.
func visualToRuneCol(cells []transcriptCell, visX int) int {
	x := 0
	for i, c := range cells {
		if x >= visX {
			return i
		}
		x += c.w
	}
	return len(cells)
}
