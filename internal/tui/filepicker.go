package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	fpMaxEntries  = 100_000
	fpBatchSize   = 256
	fpMaxVisible  = 12 // rows shown in the overlay list
	fpMinWidth    = 20
	fpMinHeight   = 5
	fpQueryPrompt = " @ "

	// Directories skipped during async repo walk.
	// These are never interesting as context references and slow the scan considerably.
)

// skipDirs contains directory names that should be excluded from the file walk.
var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	"__pycache__":  true,
	".cache":       true,
	".DS_Store":    true,
	"coverage":     true,
	".next":        true,
	".turbo":       true,
	"target":       true, // Rust/Maven
	".gradle":      true,
	"pkg":          false, // Go pkg is useful — don't skip
	".terraform":   true,
}

// filePicker is a value-type sub-model for the @-triggered fuzzy file/dir overlay.
// It follows the Elm architecture: all state is immutable between Update cycles.
// The goroutine writes to scanCh; the main loop reads it via tea.Cmd, so
// there is no direct model mutation from goroutines.
type filePicker struct {
	root     string
	entries  []string // raw discovered paths (relative to root)
	filtered []entry  // scored + sorted results for current query
	cursor   int      // index in filtered
	scanning bool     // true while the async walk is running

	// scanCh is shared across value copies so the goroutine can still deliver
	// to the channel even after the model has been replaced. This is intentional
	// and safe: only tea.Cmd reads from it; no goroutine writes to the model.
	scanCh chan []string
	cancel context.CancelFunc

	width  int
	height int // usable height for the list rows
}

// entry pairs a path with its fuzzy match score.
type entry struct {
	path  string
	score int
}

// newFilePicker creates a ready-to-scan file picker rooted at root.
func newFilePicker(root string, width, height int) filePicker {
	return filePicker{
		root:     root,
		scanCh:   make(chan []string, 32), // buffer avoids goroutine stall
		width:    max(fpMinWidth, width),
		height:   max(fpMinHeight, height),
		scanning: true,
	}
}

// startScan launches the async directory walk and returns the first tea.Cmd
// that will read its initial batch. Callers must store the returned filePicker
// (its cancel field is set here).
func (fp *filePicker) startScan() tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	fp.cancel = cancel
	ch := fp.scanCh // capture before the goroutine copies fp
	root := fp.root
	go walkEntries(ctx, root, ch, fpBatchSize, fpMaxEntries)
	return readNextBatch(ch)
}

// stopScan cancels the background walk if it is still running.
// Safe to call multiple times — context cancellation is idempotent.
func (fp filePicker) stopScan() {
	if fp.cancel != nil {
		fp.cancel()
	}
}

// refilter scores all known entries against query and rebuilds fp.filtered.
// Cursor is clamped to the new result set length.
func (fp *filePicker) refilter(query string) {
	if query == "" {
		// Show all entries in discovery order (capped to fpMaxVisible*4 for speed)
		cap := len(fp.entries)
		if cap > fpMaxVisible*4 {
			cap = fpMaxVisible * 4
		}
		fp.filtered = make([]entry, cap)
		for i, e := range fp.entries[:cap] {
			fp.filtered[i] = entry{path: e, score: 0}
		}
	} else {
		fp.filtered = fp.filtered[:0]
		for _, e := range fp.entries {
			if s := fuzzyScore(e, query); s > 0 {
				fp.filtered = append(fp.filtered, entry{path: e, score: s})
			}
		}
		sort.Slice(fp.filtered, func(i, j int) bool {
			return fp.filtered[i].score > fp.filtered[j].score
		})
	}
	// Clamp cursor
	if fp.cursor >= len(fp.filtered) {
		fp.cursor = max(0, len(fp.filtered)-1)
	}
}

// selected returns the currently highlighted path, or "" if the list is empty.
func (fp filePicker) selected() string {
	if len(fp.filtered) == 0 || fp.cursor < 0 || fp.cursor >= len(fp.filtered) {
		return ""
	}
	return fp.filtered[fp.cursor].path
}

// view renders the file picker overlay as a self-contained string.
// It is pure: no state is mutated.
func (fp filePicker) view(query string) string {
	w := fp.width
	h := fp.height

	var b strings.Builder

	// Query line
	queryLine := fpQueryStyle.Render(fpQueryPrompt) + query
	if fp.scanning {
		queryLine += fpStatusStyle.Render(" ⣾")
	}
	b.WriteString(queryLine + "\n")

	// Divider
	b.WriteString(fpDimStyle.Render(strings.Repeat("─", max(1, w-4))) + "\n")

	// Entry list
	visible := fp.filtered
	start := 0
	if len(visible) > fpMaxVisible {
		// Scroll so cursor stays in view
		if fp.cursor >= fpMaxVisible {
			start = fp.cursor - fpMaxVisible + 1
		}
		visible = visible[start : start+fpMaxVisible]
	}

	maxPath := max(1, w-6)
	for i, e := range visible {
		absIdx := i + start
		rel := e.path
		if len(rel) > maxPath {
			rel = "…" + rel[len(rel)-maxPath+1:]
		}
		line := " " + rel
		if absIdx == fp.cursor {
			line = fpSelectedStyle.Render(fmt.Sprintf(" %-*s ", maxPath, rel))
		} else {
			line = fpDimStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}

	if len(fp.entries) == 0 && !fp.scanning {
		b.WriteString(fpStatusStyle.Render("  (no files found)") + "\n")
	} else if len(fp.filtered) == 0 && query != "" {
		b.WriteString(fpStatusStyle.Render("  (no matches)") + "\n")
	}

	// Pad remaining rows so the overlay height is stable
	rendered := strings.Count(b.String(), "\n")
	for rendered < h-3 {
		b.WriteString("\n")
		rendered++
	}

	// Status line
	status := fmt.Sprintf("  %d/%d", len(fp.filtered), len(fp.entries))
	if fp.scanning {
		status += " scanning…"
	}
	b.WriteString(fpStatusStyle.Render(status) + "\n")
	b.WriteString(fpDimStyle.Render("  [↑↓] navigate  [Enter] insert  [Esc] cancel") + "\n")

	inner := b.String()
	// Render inside a rounded border, clipped to available width
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("12")).
		Width(max(fpMinWidth, w-4)).
		Render(inner)
}

// walkEntries is the goroutine that scans the repo and sends batches to ch.
// It respects ctx cancellation and stops at maxEntries.
func walkEntries(ctx context.Context, root string, ch chan<- []string, batchSize, maxEntries int) {
	defer close(ch)

	batch := make([]string, 0, batchSize)
	total := 0

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries silently
		}

		select {
		case <-ctx.Done():
			return filepath.SkipAll
		default:
		}

		// Skip top-level root itself
		if path == root {
			return nil
		}

		name := d.Name()

		// Skip hidden dot-dirs (except the root might be a dot-dir)
		if d.IsDir() && strings.HasPrefix(name, ".") && path != root {
			return filepath.SkipDir
		}

		if d.IsDir() && skipDirs[name] {
			return filepath.SkipDir
		}

		// Compute relative path
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}

		batch = append(batch, rel)
		total++

		if len(batch) >= batchSize {
			select {
			case ch <- batch:
				batch = make([]string, 0, batchSize)
			case <-ctx.Done():
				return filepath.SkipAll
			}
		}

		if total >= maxEntries {
			return filepath.SkipAll
		}

		return nil
	})
	_ = err // fire-and-forget: WalkDir errors after SkipAll are benign

	// Flush remaining
	if len(batch) > 0 {
		select {
		case ch <- batch:
		case <-ctx.Done():
		}
	}
}

// readNextBatch returns a tea.Cmd that reads the next batch from ch.
// When ch is closed, it returns filePickerDoneMsg.
func readNextBatch(ch chan []string) tea.Cmd {
	return func() tea.Msg {
		batch, ok := <-ch
		if !ok {
			return filePickerDoneMsg{}
		}
		// Return a defensive copy so the goroutine's slice is not aliased.
		cp := make([]string, len(batch))
		copy(cp, batch)
		return filePickerBatchMsg{entries: cp}
	}
}

// fuzzyScore returns a positive score if query is a fuzzy subsequence of path,
// 0 if there is no match. Higher score = better match.
//
// Scoring bonuses:
//   - Consecutive matched characters: +2 per continuation
//   - Match starts at a path separator boundary: +4
//   - Match is in the basename: +6
//
// Penalty:
//   - Each path separator in the full path: -1 (prefer shallow files)
func fuzzyScore(path, query string) int {
	if query == "" {
		return 1
	}

	lp := strings.ToLower(path)
	lq := strings.ToLower(query)

	// Subsequence check
	qi := 0
	score := 0
	prevMatch := false
	for pi := 0; pi < len(lp) && qi < len(lq); pi++ {
		if lp[pi] == lq[qi] {
			score += 1
			if prevMatch {
				score += 2 // consecutive bonus
			}
			if pi == 0 || lp[pi-1] == '/' || lp[pi-1] == os.PathSeparator {
				score += 4 // boundary bonus
			}
			prevMatch = true
			qi++
		} else {
			prevMatch = false
		}
	}

	if qi < len(lq) {
		return 0 // not a full subsequence match
	}

	// Basename bonus: full query is a subsequence of just the base name
	base := filepath.Base(path)
	lb := strings.ToLower(base)
	bqi := 0
	for _, ch := range lb {
		if bqi < len(lq) && byte(ch) == lq[bqi] {
			bqi++
		}
	}
	if bqi == len(lq) {
		score += 6
	}

	// Depth penalty
	depth := strings.Count(path, string(os.PathSeparator))
	score -= depth

	if score < 1 {
		score = 1 // any match is at least 1
	}

	return score
}
