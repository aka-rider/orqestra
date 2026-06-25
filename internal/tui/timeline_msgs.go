package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// timelineAutoscrollMsg drives continuous scroll while a drag holds beyond the
// timeline edge. Stale ticks (seq mismatch) are no-ops.
type timelineAutoscrollMsg struct{ seq uint64 }

const timelineBlinkInterval = 500 * time.Millisecond

// timelineBlinkMsg is the tagged self-tick for the live cursor blink. A tick
// whose tag no longer matches (after Stop/Start) is ignored, so the loop dies.
type timelineBlinkMsg struct{ tag int }

// blinkCmd returns the next tagged blink tick.
func blinkCmd(tag int) tea.Cmd {
	return tea.Tick(timelineBlinkInterval, func(time.Time) tea.Msg {
		return timelineBlinkMsg{tag: tag}
	})
}

// clamp constrains v to [lo, hi].
func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }
