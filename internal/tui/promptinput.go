package tui

import (
	"strconv"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"

	"rune/pkg/editor/display"
	"rune/pkg/ui/components/textedit"
	"rune/pkg/ui/styles"
)

// sentinelStart / sentinelEnd are private-use Unicode runes that bracket chip
// sentinel tokens in the buffer. PlainSync never produces them.
const (
	sentinelStart = ''
	sentinelEnd   = ''
)

// pillStyle renders a paste pill with the accent colour scheme.
// Declared here (was in smart_input.go); consumed by View() and tests.
var pillStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("0")).
	Background(lipgloss.Color("12")).
	Bold(true).
	MarginRight(1)

// pasteThreshold is the maximum line count for inline pasting.
const pasteThreshold = 7

// PromptInput is a multi-line text editor with paste-pill (chip) support.
// It embeds textedit.Model for all base editing behaviour and overrides View()
// to render large pastes as styled pills — exactly how markdownedit extends
// textedit. Only promptinput.go and runeui.go import rune packages.
type PromptInput struct {
	textedit.Model
	st          styles.Styles     // stored for View() rendering
	pastes      map[string]string // chipID (decimal) → full pasted text
	nextID      int               // monotonically increasing chip counter
	placeholder string            // shown with selection style when buffer is empty
}

// SetPlaceholder sets the placeholder text shown when the buffer is empty.
func (p PromptInput) SetPlaceholder(text string) PromptInput {
	p.placeholder = text
	return p
}

// newPromptInput constructs a focused, multi-line PromptInput.
func newPromptInput(ui runeUI) PromptInput {
	te := textedit.New(ui.keys, ui.styles,
		textedit.WithSyncFunc(textedit.PlainSync),
		textedit.WithRegistry(ui.registry),
		textedit.WithResolver(ui.resolver),
	)
	te = te.SetFocused(true)
	return PromptInput{
		Model:  te,
		st:     ui.styles,
		pastes: make(map[string]string),
	}
}

// Value returns the fully assembled prompt text: sentinel chip tokens are
// expanded to their stored paste content. This is what the LLM receives.
func (p PromptInput) Value() string {
	content := p.Content()
	if !strings.ContainsRune(content, sentinelStart) {
		return content
	}
	var b strings.Builder
	b.Grow(len(content))
	i := 0
	for i < len(content) {
		r, sz := utf8.DecodeRuneInString(content[i:])
		if r == sentinelStart {
			j := strings.IndexRune(content[i+sz:], sentinelEnd)
			if j >= 0 {
				id := content[i+sz : i+sz+j]
				if text, ok := p.pastes[id]; ok {
					b.WriteString(text)
					i += sz + j + utf8.RuneLen(sentinelEnd)
					continue
				}
			}
		}
		b.WriteRune(r)
		i += sz
	}
	return b.String()
}

// chipRange describes a sentinel token's byte location in the buffer.
type chipRange struct {
	id    string
	start int // byte offset of sentinelStart rune
	end   int // byte offset after sentinelEnd rune
}

// findChips scans Content() and returns all chip sentinel ranges in order.
func (p PromptInput) findChips() []chipRange {
	content := p.Content()
	if !strings.ContainsRune(content, sentinelStart) {
		return nil
	}
	var out []chipRange
	i := 0
	for i < len(content) {
		r, sz := utf8.DecodeRuneInString(content[i:])
		if r == sentinelStart {
			start := i
			i += sz
			j := strings.IndexRune(content[i:], sentinelEnd)
			if j >= 0 {
				id := content[i : i+j]
				endOff := i + j + utf8.RuneLen(sentinelEnd)
				out = append(out, chipRange{id: id, start: start, end: endOff})
				i = endOff
				continue
			}
		}
		_, sz = utf8.DecodeRuneInString(content[i:])
		i += sz
	}
	return out
}

// Update routes messages through chip pre-emption before delegating to textedit.
func (p PromptInput) Update(msg tea.Msg) (PromptInput, tea.Cmd) {
	// 1. Intercept paste before textedit sees it.
	switch pm := msg.(type) {
	case tea.PasteMsg:
		return p.handlePaste(string(pm.Content))
	case tea.ClipboardMsg:
		return p.handlePaste(pm.Content)
	}

	// 2. Chip pre-emption for key messages — must happen before textedit.Update
	//    mutates the buffer so we can pre-empt Backspace/Delete/printable.
	if km, ok := msg.(tea.KeyPressMsg); ok {
		chips := p.findChips()
		cursor := p.CursorOffset()
		for _, c := range chips {
			switch {
			case km.Code == tea.KeyBackspace:
				// Backspace removes the char before cursor; if that char is part
				// of this chip's sentinel, remove the entire chip.
				if cursor > c.start && cursor <= c.end {
					delete(p.pastes, c.id)
					p.Model, _ = p.Model.ReplaceRange(c.start, c.end, "") // safe: range derived from live buffer scan
					return p, nil
				}
			case km.Code == tea.KeyDelete:
				// Delete removes char at cursor; if cursor is on any sentinel byte, remove chip.
				if cursor >= c.start && cursor < c.end {
					delete(p.pastes, c.id)
					p.Model, _ = p.Model.ReplaceRange(c.start, c.end, "") // safe: range derived from live buffer scan
					return p, nil
				}
			default:
				// Swallow printable input typed inside a chip.
				if cursor > c.start && cursor < c.end && km.Text != "" {
					return p, nil
				}
			}
		}
	}

	// 3. Delegate everything else to textedit.
	var cmd tea.Cmd
	p.Model, cmd = p.Model.Update(msg)
	return p, cmd
}

// handlePaste inserts pasted content: small pastes go inline, large ones
// become chip sentinels stored in p.pastes.
//
// textedit.ReplaceRange positions the cursor at start+runeCount(text) rather than
// start+len(text), so for multi-byte text we follow each insertion with a no-op
// ReplaceRange at the correct byte end to fix the cursor position.
func (p PromptInput) handlePaste(content string) (PromptInput, tea.Cmd) {
	if content == "" {
		return p, nil
	}
	lines := strings.Count(content, "\n") + 1
	if lines <= pasteThreshold {
		off := p.CursorOffset()
		p.Model, _ = p.Model.ReplaceRange(off, off, content) // safe: cursor offset always valid
		if utf8.RuneCountInString(content) != len(content) {
			p.Model, _ = p.Model.ReplaceRange(off+len(content), off+len(content), "") // safe: cursor fixup
		}
		return p, nil
	}
	p.nextID++
	id := strconv.Itoa(p.nextID)
	p.pastes[id] = content
	sentinel := string(sentinelStart) + id + string(sentinelEnd)
	off := p.CursorOffset()
	p.Model, _ = p.Model.ReplaceRange(off, off, sentinel) // safe: cursor offset always valid
	// Fix cursor: sentinel runes are multi-byte, so runeCount != byteLen.
	p.Model, _ = p.Model.ReplaceRange(off+len(sentinel), off+len(sentinel), "") // safe: cursor fixup
	return p, nil
}

// NaturalHeight returns the natural content height at the given width.
// Used by DesiredInputHeight in screen_prompt.go.
func (p PromptInput) NaturalHeight(width int) int {
	// Fast path: the textedit model keeps an up-to-date snapshot at its own
	// configured width. NaturalContentHeight rebuilds the full layout from
	// scratch every call (O(n)); reusing TotalRows makes this O(1) and avoids
	// O(n²) behaviour when the caller queries height repeatedly during typing.
	// Snapshot().TotalRows also reflects the actual cursor-reveal state, which
	// is more accurate than NaturalContentHeight's cursor-at-0 approximation.
	if width == p.Model.Width() {
		if rows := p.Model.Snapshot().TotalRows; rows > 0 {
			return rows
		}
	}
	return p.Model.NaturalContentHeight(width)
}

// SetRect sets the component's screen rectangle (propagated to textedit).
func (p PromptInput) SetRect(r textedit.Rect) PromptInput {
	p.Model = p.Model.SetRect(r)
	return p
}

// SetFocused shadows textedit.SetFocused to return PromptInput.
func (p PromptInput) SetFocused(f bool) PromptInput {
	p.Model = p.Model.SetFocused(f)
	return p
}

// Reset clears content, pastes, and nextID counter.
func (p PromptInput) Reset() PromptInput {
	p.Model = p.Model.SetContent("")
	p.pastes = make(map[string]string)
	p.nextID = 0
	return p
}

// SetValue replaces the buffer content with a plain text string.
// Existing chips are discarded.
func (p PromptInput) SetValue(v string) PromptInput {
	p.pastes = make(map[string]string)
	p.nextID = 0
	p.Model = p.Model.SetContent(v)
	return p
}

// View overrides textedit.View with a chip-aware cell renderer, exactly as
// markdownedit overrides textedit. Render is pure: repeated calls with the
// same model state produce identical output.
func (p PromptInput) View() string {
	if p.Width() == 0 || p.Height() == 0 {
		return ""
	}

	// Placeholder: shown when buffer is empty, styled as pre-selected text so
	// the user knows typing will replace it immediately.
	if p.Content() == "" && p.placeholder != "" {
		phLines := strings.SplitN(p.placeholder, "\n", p.Height()+1)
		contentH := p.ContentHeight()
		sel := p.st.Selection
		rendered := make([]string, 0, contentH)
		for i := 0; i < contentH; i++ {
			if i < len(phLines) {
				rendered = append(rendered, sel.Render(phLines[i]))
			} else {
				rendered = append(rendered, "")
			}
		}
		return lipgloss.NewStyle().
			MaxWidth(p.Width()).MaxHeight(p.Height()).
			Width(p.Width()).Height(p.Height()).
			Render(strings.Join(rendered, "\n"))
	}

	// Delegate to textedit's render pipeline with a chip-aware cell builder.
	// RenderView handles: cursor overlay, selection overlay, horizontal scroll,
	// tilde fill, dim-when-unfocused, and outer dimension wrap.
	chips := p.findChips()
	cellBuilder := func(sp display.DisplaySpan) []textedit.Cell {
		if cr := chipForSpan(sp.BufferStart, chips); cr != nil {
			label := pillLabelFor(cr.id, p.pastes)
			return chipCells(label, cr.start, pillStyle)
		}
		return textedit.SpanToCells(sp, lipgloss.NewStyle())
	}
	return p.Model.RenderView(cellBuilder, nil)
}

// chipForSpan returns the chipRange whose sentinel byte range contains bufStart, or nil.
func chipForSpan(bufStart int, chips []chipRange) *chipRange {
	for i := range chips {
		if bufStart >= chips[i].start && bufStart < chips[i].end {
			return &chips[i]
		}
	}
	return nil
}

// pillLabelFor builds the display label for a chip from the pastes map.
func pillLabelFor(id string, pastes map[string]string) string {
	text, ok := pastes[id]
	if !ok {
		return "[Pasted text]"
	}
	lines := strings.Count(text, "\n") + 1
	return "[Pasted text #" + id + " +" + strconv.Itoa(lines) + " lines]"
}

// chipCells produces cells for a pill label string. All cells carry BufOffset
// set to bufStart so ApplyOverlays can mark the cursor on the chip correctly.
func chipCells(label string, bufStart int, st lipgloss.Style) []textedit.Cell {
	cells := make([]textedit.Cell, 0, len(label))
	for _, r := range label {
		w := runewidth.RuneWidth(r)
		if w == 0 {
			w = 1
		}
		cells = append(cells, textedit.Cell{
			Rune:      r,
			Width:     w,
			Style:     st,
			BufOffset: bufStart,
		})
	}
	return cells
}
