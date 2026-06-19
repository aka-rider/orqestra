package tui

import (
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// PromptScreen manages the task prompt input and file picker.
type PromptScreen struct {
	input SmartInput
	fp    filePicker
	fpActive      bool
	fpAtStart     int
	fpQuery       string
	width         int     // set by parent for layout calculations
	height        int     // set by parent for layout calculations
	PendingIntent tea.Msg // set by Update, consumed by parent
}

// NewPromptScreen creates a new prompt screen with initialized smart input.
func NewPromptScreen() PromptScreen {
	return PromptScreen{
		input: NewSmartInput(),
	}
}

// Focus focuses the input.
func (s *PromptScreen) Focus() {}

// Reset resets the input value.
func (s *PromptScreen) Reset() { s.input.Reset() }

// SetValue sets the input value.
func (s *PromptScreen) SetValue(v string) {
	// Insert as a single text segment.
	s.input = NewSmartInput()
	s.input.insertSegAtCursor(textSegment{text: v})
}

// Value returns the input value.
func (s PromptScreen) Value() string { return s.input.Value() }

// SetWidth sets the input width.
func (s *PromptScreen) SetWidth(w int) { s.input.width = w }

// SetTextareaHeight explicitly sets the height of the input.
// Kept for API compatibility; the SmartInput computes its own height.
func (s *PromptScreen) SetTextareaHeight(h int) {
	s.input.height = h
}

// DesiredInputHeight calculates the desired height for the input zone based on
// its content, capped at half the terminal height.
func (s *PromptScreen) DesiredInputHeight(termHeight int) int {
	w := s.input.width
	if w <= 0 {
		return constPromptInputHeight
	}

	// Count rendered lines (pills count as 1 line each).
	lines := s.input.desiredLineCount(w)

	// Calculate desired total height including chrome (divider + instruction label)
	chrome := 2
	// Minimum height is chrome + 3 line textarea
	desired := max(constPromptInputHeight, lines+chrome)

	// Cap at half terminal height
	maxHeight := max(constPromptInputHeight, termHeight/2)
	return min(desired, maxHeight)
}

// Update handles key events for the prompt screen.
func (s PromptScreen) Update(msg tea.Msg) (PromptScreen, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		// Pass non-key messages to smart input (e.g., paste, blink)
		var cmd tea.Cmd
		s.input, cmd = s.input.Update(msg)
		return s, cmd
	}

	if s.fpActive {
		return s.handleFilePickerKey(keyMsg)
	}

	// Ctrl combos first
	switch keyMsg.String() {
	case "ctrl+r":
		s.PendingIntent = NavigateToRunsListIntent{}
		return s, nil
	case "ctrl+p":
		s.PendingIntent = ToggleSetupIntent{}
		return s, nil
	}

	switch keyMsg.Code {
	case tea.KeyEnter:
		if keyMsg.Mod.Contains(tea.ModShift) || keyMsg.Mod.Contains(tea.ModAlt) {
			// Shift+Enter / Alt+Enter inserts a newline
			s.input.insertSegAtCursor(textSegment{text: "\n"})
			return s, nil
		}
		prompt := strings.TrimSpace(s.input.Value())
		if prompt == "" {
			return s, nil
		}
		s.PendingIntent = StartPipelineIntent{Prompt: prompt}
		return s, nil
	default:
		var cmd tea.Cmd
		s.input, cmd = s.input.Update(msg)
		if !s.fpActive && keyMsg.String() == "@" {
			return s.activateFilePicker(cmd)
		}
		return s, cmd
	}
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

	// Footer (2 lines)
	footer := dividerStyle.Render(strings.Repeat("─", w)) + "\n" +
		keyStyle.Render(" [Enter] submit | [Shift+Enter] newline | [^P] setup  [^R] runs  [^C] quit")

	// Input zone (divider + instruction + smart input + newline)
	input := dividerStyle.Render(strings.Repeat("─", w)) + "\n" +
		" Enter a task description. Be specific about the end state.\n" +
		s.input.View(w, height) + "\n"

	// Content zone dimensions — no header, no sidebar in prompt view
	inputHeight := s.DesiredInputHeight(height)
	contentHeight := max(0, height-inputHeight-constFooterHeight)

	// If content zone too small, skip split view — just render chrome
	if contentHeight < 2 {
		return input + footer
	}

	var body string
	if s.fpActive {
		pickerStr := s.fp.view(s.fpQuery)
		body = lipgloss.Place(w, contentHeight, lipgloss.Left, lipgloss.Bottom, pickerStr)
	} else {
		// Content: mascot art centered vertically, full-width
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

	return body + "\n" + input + footer
}

// handleFilePickerKey processes key events while the file picker overlay is active.
func (s PromptScreen) handleFilePickerKey(msg tea.KeyPressMsg) (PromptScreen, tea.Cmd) {
	switch msg.Code {
	case tea.KeyEscape:
		s.fp.stopScan()
		s.fpActive = false
		val := s.input.Value()
		if s.fpAtStart < len(val) {
			s.input = s.input.WithValue(val[:s.fpAtStart] + val[s.fpAtStart+1+len(s.fpQuery):])
		}
		s.fpQuery = ""
		return s, nil

	case tea.KeyEnter:
		sel := s.fp.selected()
		s.fp.stopScan()
		s.fpActive = false
		if sel != "" {
			val := s.input.Value()
			before := val[:s.fpAtStart]
			after := ""
			end := s.fpAtStart + 1 + len(s.fpQuery)
			if end < len(val) {
				after = val[end:]
			}
			s.input = s.input.WithValue(before + sel + " " + after)
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
			s.fpQuery = s.fpQuery[:len(s.fpQuery)-1]
			s.fp.refilter(s.fpQuery)
		} else {
			s.fp.stopScan()
			s.fpActive = false
			val := s.input.Value()
			if s.fpAtStart < len(val) {
				s.input = s.input.WithValue(val[:s.fpAtStart] + val[s.fpAtStart+1:])
			}
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
	s.fpAtStart = len(s.input.Value()) - 1
	s.fpQuery = ""
	scanCmd := s.fp.startScan()
	if pendingCmd != nil {
		return s, tea.Batch(pendingCmd, scanCmd)
	}
	return s, scanCmd
}
