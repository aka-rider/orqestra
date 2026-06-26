package frame

import tea "charm.land/bubbletea/v2"

// StaticFrame is an immutable transcript item. It lays itself out into display
// rows at a width and renders nothing else — the Timeline owns scrolling and
// selection. Re-layout happens in SetWidth (an Update-path call), never in a
// render path, so producing Rows() is a pure read.
//
// Value semantics: SetWidth returns the updated frame; the Timeline stores the
// returned copy.
type StaticFrame interface {
	SetWidth(w int) StaticFrame
	Rows() []Row
}

// InteractiveFrame is the single live tail of the Timeline: the in-progress
// unit. It accumulates output, may tick (a blinking cursor), and resolves into
// a StaticFrame when the unit completes. At most one is active at a time.
type InteractiveFrame interface {
	SetWidth(w int) InteractiveFrame
	Update(msg tea.Msg) (InteractiveFrame, tea.Cmd)
	Rows() []Row
	Resolve() StaticFrame
}

// Height reports a frame's row count at its current width.
func Height(f StaticFrame) int { return len(f.Rows()) }
