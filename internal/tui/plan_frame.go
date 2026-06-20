package tui

import (
	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"

	"rune/pkg/editor/display"
	"rune/pkg/ui/components/markdownedit"
	"rune/pkg/ui/components/textedit"
	"rune/pkg/ui/styles"
)

// planFrame is the Timeline's Plan Frame: a retained read-only markdownedit
// used purely as a renderer. It builds timelineRows from the display snapshot
// rather than from markdownedit.View() (which returns an opaque styled string).
//
// Resize protocol (must be followed in Update paths, never in View):
//  1. SetRect({W:w, H:largeH}) to sync at width w
//  2. n := Snapshot().TotalRows
//  3. SetRect({W:w, H:n}) for exact natural height (no ~ padding, no scroll)
type planFrame struct {
	md          markdownedit.Model
	rawMarkdown string
	st          styles.Styles
	cachedRows  []timelineRow
	cachedWidth int
}

// newPlanFrame creates a retained read-only markdownedit for the given markdown.
func newPlanFrame(markdown string, ui runeUI) planFrame {
	md := markdownedit.New(ui.keys, ui.styles, ui.caps,
		markdownedit.WithRegistry(ui.registry),
		markdownedit.WithResolver(ui.resolver),
	)
	md = md.SetReadOnly(true)
	md = md.SetFocused(false)
	md = md.SetContent(markdown)
	return planFrame{
		md:          md,
		rawMarkdown: markdown,
		st:          ui.styles,
	}
}

// resize re-syncs the markdownedit at the given width and rebuilds cached rows.
// Must be called from an Update path.
func (pf *planFrame) resize(w int) {
	if w <= 0 {
		pf.cachedRows = nil
		pf.cachedWidth = 0
		return
	}
	// Step 1: sync at width w with a large height to avoid internal scroll.
	pf.md = pf.md.SetRect(textedit.Rect{W: w, H: 10000})
	// Step 2: read exact natural height.
	n := pf.md.Snapshot().TotalRows
	if n <= 0 {
		pf.cachedRows = nil
		pf.cachedWidth = w
		return
	}
	// Step 3: set exact height (no ~ padding, no internal scroll at H==TotalRows).
	pf.md = pf.md.SetRect(textedit.Rect{W: w, H: n})
	// Build rows from the display snapshot.
	pf.cachedRows = frameRowsFromDisplay(pf.md.Snapshot(), pf.st)
	pf.cachedWidth = w
}

// height returns the number of display rows at the current cached width.
func (pf planFrame) height() int { return len(pf.cachedRows) }

// rows returns the cached display rows. Caller must have called resize(w) first.
func (pf planFrame) rows() []timelineRow { return pf.cachedRows }

// frameRowsFromDisplay maps each DisplayLine in the snapshot to a timelineRow.
// Rows are tagged opaque (line-granular selection; copy emits rawMarkdown).
func frameRowsFromDisplay(snap display.DisplaySnapshot, st styles.Styles) []timelineRow {
	n := snap.TotalRows
	if n <= 0 {
		return nil
	}
	lines := snap.Slice(0, n)
	rows := make([]timelineRow, 0, n)
	for i, dl := range lines {
		cells := displayLineToCells(dl, st)
		rows = append(rows, timelineRow{
			lineIdx:  i,
			startCol: 0,
			cells:    cells,
			opaque:   true,
		})
	}
	return rows
}

// displayLineToCells converts a display.DisplayLine into timelineCells.
// This replicates the core of markdownedit.spanToCellsStyled for the span
// kinds that appear in plan documents.
func displayLineToCells(dl display.DisplayLine, st styles.Styles) []timelineCell {
	var cells []timelineCell
	for _, sp := range dl.Spans {
		style := planSpanStyle(sp, st)
		for _, r := range sp.Text {
			if r == '\n' || r == '\r' {
				continue
			}
			w := runewidth.RuneWidth(r)
			if w == 0 {
				w = 1
			}
			cells = append(cells, timelineCell{r: r, w: w, style: style})
		}
	}
	return cells
}

// planSpanStyle maps a display span to a lipgloss.Style for plan frame rendering.
func planSpanStyle(sp display.DisplaySpan, st styles.Styles) lipgloss.Style {
	// Table spans: use role-based style.
	if sp.Kind == display.TokenTable {
		switch sp.TableRole {
		case display.TableRoleHeader:
			return st.TableHeader
		case display.TableRoleSeparator:
			return st.TableSeparator
		default:
			return st.TableBody
		}
	}
	// Link spans.
	switch sp.LinkRole() {
	case display.LinkRoleNavigable:
		return st.Link
	case display.LinkRoleImage:
		return lipgloss.NewStyle()
	}
	// Semantic spans.
	switch sp.Kind {
	case display.TokenHeading:
		switch sp.HeadingLevel {
		case 1:
			return st.HeadingH1
		case 2:
			return st.HeadingH2
		case 3:
			return st.HeadingH3
		default:
			return st.HeadingH2
		}
	case display.TokenInlineCode:
		return st.InlineCode
	case display.TokenCodeFence:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	case display.TokenBold:
		return st.MdBold
	case display.TokenItalic:
		return st.MdItalic
	case display.TokenStrikethrough:
		return st.MdStrikethrough
	case display.TokenBlockquote:
		return st.MdBlockquote
	case display.TokenHorizontalRule:
		return st.HorizontalRule
	case display.TokenListMarker, display.TokenTaskList:
		return st.ListMarker
	case display.TokenTag:
		return st.Tag
	}
	return lipgloss.NewStyle()
}
