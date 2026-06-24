package frame

import (
	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"

	"rune/pkg/command"
	"rune/pkg/editor/display"
	"rune/pkg/editor/keybind"
	"rune/pkg/terminal"
	"rune/pkg/ui/components/markdownedit"
	"rune/pkg/ui/components/textedit"
	rkeymap "rune/pkg/ui/keymap"
	rstyles "rune/pkg/ui/styles"
)

// MDDeps bundles the rune markdownedit dependencies a Plan frame needs. The
// Timeline builds it once from its runeUI and passes it in, keeping the frame
// package's rune coupling explicit and in one place.
type MDDeps struct {
	Keys     rkeymap.Bindings
	Styles   rstyles.Styles
	Registry command.Registry
	Resolver keybind.Resolver
	Caps     terminal.TermCaps
}

// Plan is the gate artifact: a retained read-only markdownedit used purely as a
// renderer, plus the raw markdown for open-in-$EDITOR. It lays out via the
// display snapshot, never via markdownedit.View() (an opaque styled string).
type Plan struct {
	md          markdownedit.Model
	rawMarkdown string
	st          rstyles.Styles
	rows        []Row
}

// NewPlan builds a read-only markdownedit for the given markdown.
func NewPlan(markdown string, deps MDDeps) Plan {
	md := markdownedit.New(deps.Keys, deps.Styles, deps.Caps,
		markdownedit.WithRegistry(deps.Registry),
		markdownedit.WithResolver(deps.Resolver),
	)
	md = md.SetReadOnly(true)
	md = md.SetFocused(false)
	md = md.SetContent(markdown)
	return Plan{md: md, rawMarkdown: markdown, st: deps.Styles}
}

// RawMarkdown returns the plan source for open-in-$EDITOR. Concrete-only — not
// on the StaticFrame interface (selection copies rendered text, per the rehaul).
func (p Plan) RawMarkdown() string { return p.rawMarkdown }

// SetWidth re-syncs the markdownedit at width w to its exact natural height and
// rebuilds the display rows. Two-step rect (large H, then exact H) avoids any
// internal scroll/padding. Relayout lives here, in the Update path, not View.
func (p Plan) SetWidth(w int) StaticFrame {
	if w <= 0 {
		p.rows = nil
		return p
	}
	p.md = p.md.SetRect(textedit.Rect{W: w, H: 10000})
	n := p.md.Snapshot().TotalRows
	if n <= 0 {
		p.rows = nil
		return p
	}
	p.md = p.md.SetRect(textedit.Rect{W: w, H: n})
	p.rows = rowsFromDisplay(p.md.Snapshot(), p.st)
	return p
}

func (p Plan) Rows() []Row { return p.rows }

// rowsFromDisplay maps each DisplayLine in the snapshot to one Row.
func rowsFromDisplay(snap display.DisplaySnapshot, st rstyles.Styles) []Row {
	n := snap.TotalRows
	if n <= 0 {
		return nil
	}
	lines := snap.Slice(0, n)
	rows := make([]Row, 0, n)
	for _, dl := range lines {
		rows = append(rows, Row{Cells: displayLineToCells(dl, st)})
	}
	return rows
}

// displayLineToCells converts a display.DisplayLine into styled cells.
func displayLineToCells(dl display.DisplayLine, st rstyles.Styles) []Cell {
	var cells []Cell
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
			cells = append(cells, Cell{R: r, W: w, Style: style})
		}
	}
	return cells
}

// planSpanStyle maps a display span to a lipgloss style for plan rendering.
func planSpanStyle(sp display.DisplaySpan, st rstyles.Styles) lipgloss.Style {
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
	switch sp.LinkRole() {
	case display.LinkRoleNavigable:
		return st.Link
	case display.LinkRoleImage:
		return lipgloss.NewStyle()
	}
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
