package tui

// scrollFollow returns a new viewport offset so cursor stays within
// [offset, offset+size), keeping ≥ margin rows from each edge. Ported
// verbatim from /Users/xiii/Developer/rune/pkg/ui/scroll/scroll.go.
func scrollFollow(cursor, offset, size, total, margin, jump int) int {
	if size <= 0 {
		return 0
	}
	if margin*2 > size {
		margin = (size - 1) / 2
	}
	if margin < 0 {
		margin = 0
	}
	if jump > size-1-2*margin {
		jump = size - 1 - 2*margin
	}
	if jump < 0 {
		jump = 0
	}

	switch {
	case cursor < offset+margin:
		offset = cursor - margin - jump
	case cursor >= offset+size-1-margin:
		offset = cursor - (size - 1 - margin) + jump
	}

	maxOff := total - size
	if maxOff < 0 {
		maxOff = 0
	}
	if offset < 0 {
		offset = 0
	}
	if offset > maxOff {
		offset = maxOff
	}
	return offset
}

// scrolloff returns the scroll margin in rows for a viewport of height h.
// Clamped to [1, 4] and approximately 8% of the height.
func scrolloff(h int) int {
	m := h * 8 / 100
	if m < 1 {
		return 1
	}
	if m > 4 {
		return 4
	}
	return m
}
