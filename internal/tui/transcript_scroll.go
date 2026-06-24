package tui

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
