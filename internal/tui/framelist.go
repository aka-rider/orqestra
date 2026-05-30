package tui

import "time"

// FrameList holds the ordered list of frames and manages dirty-flagged rendering.
// Pointer semantics: owns growing slices mutated per-tick.
type FrameList struct {
	frames     []Frame
	width      int
	dirty      bool
	lastRender string
	animFrame  int
}

// NewFrameList creates an empty frame list with the given width.
func NewFrameList(width int) *FrameList {
	return &FrameList{width: width, dirty: true}
}

// AppendFrame adds a new frame to the list and marks dirty.
func (fl *FrameList) AppendFrame(f Frame) {
	fl.frames = append(fl.frames, f)
	fl.dirty = true
}

// UpdateActive mutates the last InProgress frame via fn.
// No-op if no InProgress frame exists.
func (fl *FrameList) UpdateActive(fn func(*Frame)) {
	for i := len(fl.frames) - 1; i >= 0; i-- {
		if fl.frames[i].State == FrameInProgress {
			fn(&fl.frames[i])
			fl.dirty = true
			return
		}
	}
}

// FinishActive marks the last InProgress frame as finished with final metrics.
func (fl *FrameList) FinishActive(elapsed time.Duration, inputTok, outputTok int64) {
	for i := len(fl.frames) - 1; i >= 0; i-- {
		if fl.frames[i].State == FrameInProgress {
			fl.frames[i].State = FrameFinished
			fl.frames[i].Elapsed = elapsed
			fl.frames[i].InputTokens = inputTok
			fl.frames[i].OutputTokens = outputTok
			fl.dirty = true
			return
		}
	}
}

// Render returns the concatenated rendered string of all frames.
// Uses dirty-flag caching: only re-renders when data has changed.
func (fl *FrameList) Render() string {
	if !fl.dirty && fl.lastRender != "" {
		return fl.lastRender
	}
	fl.lastRender = fl.render()
	fl.dirty = false
	return fl.lastRender
}

// SetWidth updates the render width and marks dirty if changed.
func (fl *FrameList) SetWidth(w int) {
	if fl.width != w {
		fl.width = w
		fl.dirty = true
	}
}

// SetAnimFrame updates the shimmer animation counter.
// Only marks dirty if an InProgress frame exists (shimmer is visible).
func (fl *FrameList) SetAnimFrame(n int) {
	if fl.animFrame == n {
		return
	}
	fl.animFrame = n
	for i := range fl.frames {
		if fl.frames[i].State == FrameInProgress {
			fl.dirty = true
			return
		}
	}
}

// FrameCount returns the number of frames.
func (fl *FrameList) FrameCount() int {
	return len(fl.frames)
}

// FrameTopLine computes the rendered line offset of frame at idx.
// Used for scroll-to-frame (Alt+N). Returns 0 if idx is out of range.
func (fl *FrameList) FrameTopLine(idx int) int {
	if idx <= 0 || idx >= len(fl.frames) {
		return 0
	}
	// Render individual frames and count lines
	offset := 0
	for i := 0; i < idx; i++ {
		rendered := fl.renderSingleFrame(i)
		offset += countLines(rendered)
	}
	return offset
}

// IsDirty reports whether the frame list has uncommitted changes.
func (fl *FrameList) IsDirty() bool {
	return fl.dirty
}

// render produces the full concatenated output of all frames.
func (fl *FrameList) render() string {
	if len(fl.frames) == 0 {
		return ""
	}
	var total int
	parts := make([]string, len(fl.frames))
	for i := range fl.frames {
		parts[i] = fl.renderSingleFrame(i)
		total += len(parts[i])
	}
	// Pre-allocate and join with single newline between frames
	buf := make([]byte, 0, total+len(fl.frames))
	for i, p := range parts {
		if i > 0 {
			buf = append(buf, '\n')
		}
		buf = append(buf, p...)
	}
	return string(buf)
}

// renderSingleFrame renders one frame by index.
func (fl *FrameList) renderSingleFrame(idx int) string {
	return renderFrame(&fl.frames[idx], fl.width, fl.animFrame)
}

// countLines counts the number of newline-terminated lines in s.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := 0
	for i := range s {
		if s[i] == '\n' {
			n++
		}
	}
	// Count final line if not newline-terminated
	if len(s) > 0 && s[len(s)-1] != '\n' {
		n++
	}
	return n
}
