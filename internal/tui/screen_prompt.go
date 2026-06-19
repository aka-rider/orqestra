package tui

import (
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"rune/pkg/ui/components/textedit"
)

// PromptScreen manages the task prompt input and file picker.
type PromptScreen struct {
	input         PromptInput
	fp            filePicker
	fpActive      bool
	fpAtStart     int // byte offset of '@' in the buffer
	fpQuery       string
	width         int     // set by parent for layout calculations
	height        int     // set by parent for layout calculations
	PendingIntent tea.Msg // set by Update, consumed by parent
}

// NewPromptScreen creates a new prompt screen with a focused PromptInput.
func NewPromptScreen(ui runeUI) PromptScreen {
	return PromptScreen{
		input: newPromptInput(ui).SetPlaceholder(
			"Enter a task description. Be specific about the end state.",
		),
	}
}

// Focus is a no-op — PromptInput is always focused.
func (s *PromptScreen) Focus() {}

// Reset clears the input.
func (s *PromptScreen) Reset() { s.input = s.input.Reset() }

// SetValue replaces the input with plain text, discarding any chips.
func (s *PromptScreen) SetValue(v string) { s.input = s.input.SetValue(v) }

// Value returns the assembled prompt text (chips expanded).
func (s PromptScreen) Value() string { return s.input.Value() }

// SetWidth sets the rendering width (used by legacy callers via recalcLayout).
func (s *PromptScreen) SetWidth(w int) {
	s.input = s.input.SetRect(textedit.Rect{W: w, H: s.input.Height()})
}

// SetTextareaHeight explicitly sets the allocated height of the input zone.
func (s *PromptScreen) SetTextareaHeight(h int) {
	s.input = s.input.SetRect(textedit.Rect{W: s.input.Width(), H: h})
}

// DesiredInputHeight calculates the preferred height for the input zone based
// on content, capped at half the terminal height.
func (s *PromptScreen) DesiredInputHeight(termHeight int) int {
	w := s.input.Width()
	if w <= 0 {
		return constPromptInputHeight
	}
	lines := s.input.NaturalHeight(w)
	chrome := 1 // divider only (instruction moved into placeholder)
	desired := max(constPromptInputHeight, lines+chrome)
	maxHeight := max(constPromptInputHeight, termHeight/2)
	return min(desired, maxHeight)
}

// Update handles messages for the prompt screen.
func (s PromptScreen) Update(msg tea.Msg) (PromptScreen, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		// Non-key messages (paste, clipboard) go to PromptInput.
		var cmd tea.Cmd
		s.input, cmd = s.input.Update(msg)
		return s, cmd
	}

	if s.fpActive {
		return s.handleFilePickerKey(keyMsg)
	}

	// Reserved chords: intercept before delegating to textedit.
	switch keyMsg.String() {
	case "ctrl+r":
		s.PendingIntent = NavigateToRunsListIntent{}
		return s, nil
	case "ctrl+p":
		s.PendingIntent = ToggleSetupIntent{}
		return s, nil
	}

	// Enter: submit or insert newline — NEVER delegated to textedit.
	if keyMsg.Code == tea.KeyEnter {
		if keyMsg.Mod.Contains(tea.ModShift) || keyMsg.Mod.Contains(tea.ModAlt) {
			// Shift+Enter / Alt+Enter → insert newline at cursor.
			off := s.input.CursorOffset()
			s.input.Model = s.input.Model.ReplaceRange(off, off, "\n")
			return s, nil
		}
		prompt := strings.TrimSpace(s.input.Value())
		if prompt == "" {
			return s, nil
		}
		s.PendingIntent = StartPipelineIntent{Prompt: prompt}
		return s, nil
	}

	// Everything else goes to PromptInput (which has its own chip pre-emption).
	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	// If '@' was just typed, activate the file picker.
	if !s.fpActive && keyMsg.String() == "@" {
		return s.activateFilePicker(cmd)
	}
	return s, cmd
}

// View renders the prompt screen.
func (s PromptScreen) View(width, height int) string {
	w := width
	if w < minWidth {
		w = minWidth
	}
	if height < minHeight {
		return " Terminal too small. Please resize."
	}

	// Footer (2 lines).
	footer := dividerStyle.Render(strings.Repeat("─", w)) + "\n" +
		keyStyle.Render(" [Enter] submit | [Shift+Enter] newline | [^P] setup  [^R] runs  [^C] quit")

	// Input zone (divider + input + newline).
	inputView := dividerStyle.Render(strings.Repeat("─", w)) + "\n" +
		s.input.View() + "\n"

	// Content zone dimensions.
	inputHeight := s.DesiredInputHeight(height)
	contentHeight := max(0, height-inputHeight-constFooterHeight)

	if contentHeight < 2 {
		return inputView + footer
	}

	var body string
	if s.fpActive {
		pickerStr := s.fp.view(s.fpQuery)
		body = lipgloss.Place(w, contentHeight, lipgloss.Left, lipgloss.Bottom, pickerStr)
	} else {
		mascot := renderMascot(w-2, contentHeight)
		mascotLines := strings.Split(mascot, "\n")
		padTop := 0
		if len(mascotLines) < contentHeight {
			padTop = (contentHeight - len(mascotLines)) / 2
		}
		var contentBuf strings.Builder
		for i := 0; i < contentHeight; i++ {
			mi := i - padTop
			if mi >= 0 && mi < len(mascotLines) {
				contentBuf.WriteString(" " + mascotLines[mi])
			}
			if i < contentHeight-1 {
				contentBuf.WriteString("\n")
			}
		}
		body = contentBuf.String()
	}

	return body + "\n" + inputView + footer
}

// handleFilePickerKey processes key events while the file picker is active.
func (s PromptScreen) handleFilePickerKey(msg tea.KeyPressMsg) (PromptScreen, tea.Cmd) {
	switch msg.Code {
	case tea.KeyEscape:
		s.fp.stopScan()
		s.fpActive = false
		// Remove '@' + query from the buffer.
		removeEnd := s.fpAtStart + 1 + len(s.fpQuery)
		s.input.Model = s.input.Model.ReplaceRange(s.fpAtStart, removeEnd, "")
		s.fpQuery = ""
		return s, nil

	case tea.KeyEnter:
		sel := s.fp.selected()
		s.fp.stopScan()
		s.fpActive = false
		if sel != "" {
			// Replace '@' + query with the selected path.
			removeEnd := s.fpAtStart + 1 + len(s.fpQuery)
			s.input.Model = s.input.Model.ReplaceRange(s.fpAtStart, removeEnd, sel+" ")
		}
		s.fpQuery = ""
		return s, nil

	case tea.KeyUp:
		if s.fp.cursor > 0 {
			s.fp.cursor--
		}
		return s, nil

	case tea.KeyDown:
		if s.fp.cursor < len(s.fp.filtered)-1 {
			s.fp.cursor++
		}
		return s, nil

	case tea.KeyBackspace:
		if len(s.fpQuery) > 0 {
			// Remove last rune from query and from buffer.
			runes := []rune(s.fpQuery)
			lastRuneLen := len(s.fpQuery) - len(string(runes[:len(runes)-1]))
			cursor := s.input.CursorOffset()
			s.input.Model = s.input.Model.ReplaceRange(cursor-lastRuneLen, cursor, "")
			s.fpQuery = string(runes[:len(runes)-1])
			s.fp.refilter(s.fpQuery)
		} else {
			// No query: remove '@' from buffer and deactivate.
			s.fp.stopScan()
			s.fpActive = false
			s.input.Model = s.input.Model.ReplaceRange(s.fpAtStart, s.fpAtStart+1, "")
		}
		return s, nil

	default:
		if len(msg.Text) > 0 && msg.Code != tea.KeyBackspace && msg.Code != tea.KeyEnter {
			s.fpQuery += msg.Text
			s.fp.refilter(s.fpQuery)
			var cmd tea.Cmd
			s.input, cmd = s.input.Update(msg)
			return s, cmd
		}
	}
	return s, nil
}

// activateFilePicker initialises the file picker overlay after '@' is typed.
func (s PromptScreen) activateFilePicker(pendingCmd tea.Cmd) (PromptScreen, tea.Cmd) {
	cwd, err := os.Getwd()
	if err != nil {
		return s, pendingCmd
	}
	contentWidth := max(1, s.width)
	contentHeight := max(1, s.height-constPromptInputHeight-constFooterHeight)
	s.fp = newFilePicker(cwd, contentWidth, contentHeight)
	s.fpActive = true
	// '@' is a 1-byte ASCII char; cursor is now just after it.
	s.fpAtStart = s.input.CursorOffset() - 1
	s.fpQuery = ""
	scanCmd := s.fp.startScan()
	if pendingCmd != nil {
		return s, tea.Batch(pendingCmd, scanCmd)
	}
	return s, scanCmd
}
