package tui

import (
	"os"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// PromptScreen manages the task prompt input and file picker.
type PromptScreen struct {
	textarea      textarea.Model
	fp            filePicker
	fpActive      bool
	fpAtStart     int
	fpQuery       string
	width         int     // set by parent for layout calculations
	height        int     // set by parent for layout calculations
	PendingIntent tea.Msg // set by Update, consumed by parent
}

// NewPromptScreen creates a new prompt screen with initialized textarea.
func NewPromptScreen() PromptScreen {
	ta := textarea.New()
	ta.Placeholder = "Enter a task description. Be specific about the end state."
	ta.Focus()
	ta.SetWidth(80)
	ta.SetHeight(3)
	ta.CharLimit = 4096
	return PromptScreen{textarea: ta}
}

// Focus focuses the textarea.
func (s *PromptScreen) Focus() { s.textarea.Focus() }

// Reset resets the textarea value.
func (s *PromptScreen) Reset() { s.textarea.Reset() }

// SetValue sets the textarea value.
func (s *PromptScreen) SetValue(v string) { s.textarea.SetValue(v) }

// Value returns the textarea value.
func (s PromptScreen) Value() string { return s.textarea.Value() }

// SetWidth sets the textarea width.
func (s *PromptScreen) SetWidth(w int) { s.textarea.SetWidth(w) }

// Update handles key events for the prompt screen.
func (s PromptScreen) Update(msg tea.Msg) (PromptScreen, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		// Pass non-key messages to textarea (e.g., blink)
		var cmd tea.Cmd
		s.textarea, cmd = s.textarea.Update(msg)
		return s, cmd
	}

	if s.fpActive {
		return s.handleFilePickerKey(keyMsg)
	}

	// Ctrl combos first (no named constants in v2)
	switch keyMsg.String() {
	case "ctrl+s":
		prompt := strings.TrimSpace(s.textarea.Value())
		if prompt == "" {
			return s, nil
		}
		s.PendingIntent = StartPipelineIntent{Prompt: prompt, SkipGateway: true}
		return s, nil
	case "ctrl+r":
		s.PendingIntent = NavigateToRunsListIntent{}
		return s, nil
	}

	switch keyMsg.Code {
	case tea.KeyEnter:
		if keyMsg.Mod.Contains(tea.ModShift) || keyMsg.Mod.Contains(tea.ModAlt) {
			// Shift+Enter / Alt+Enter inserts a newline
			s.textarea.InsertString("\n")
			return s, nil
		}
		prompt := strings.TrimSpace(s.textarea.Value())
		if prompt == "" {
			return s, nil
		}
		s.PendingIntent = StartPipelineIntent{Prompt: prompt, SkipGateway: false}
		return s, nil
	default:
		var cmd tea.Cmd
		s.textarea, cmd = s.textarea.Update(msg)
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

	// Header (2 lines)
	header := headerStyle.Render(" Orqestra") + "\n" +
		dividerStyle.Render(strings.Repeat("─", w)) + "\n"

	// Footer (2 lines)
	footer := dividerStyle.Render(strings.Repeat("─", w)) + "\n" +
		keyStyle.Render(" [Enter] submit | [Shift+Enter] newline | [Ctrl+S] skip gateway | [Ctrl+R] runs  [^C^C] quit")

	// Input zone (divider + instruction + textarea + newline)
	input := dividerStyle.Render(strings.Repeat("─", w)) + "\n" +
		" Enter a task description. Be specific about the end state.\n" +
		s.textarea.View() + "\n"

	// Content zone dimensions — derived from constants
	contentHeight := max(0, height-constHeaderHeight-constPromptInputHeight-constFooterHeight)
	contentWidth := max(0, int(float64(w)*splitRatio))
	sidebarWidth := max(0, w-contentWidth-1)

	// If content zone too small, skip split view — just render chrome
	if contentHeight < 2 {
		return header + input + footer
	}

	var body string
	if s.fpActive {
		pickerStr := s.fp.view(s.fpQuery)
		body = lipgloss.Place(w, contentHeight, lipgloss.Left, lipgloss.Bottom, pickerStr)
	} else {
		// Content: mascot art centered vertically
		mascot := renderMascot(contentWidth-2, contentHeight)
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

		// Sidebar: static agent list
		var sidebarBuf strings.Builder
		sidebarBuf.WriteString(" Agents\n")
		sidebarBuf.WriteString(strings.Repeat("─", max(1, sidebarWidth-1)) + "\n")
		sidebarBuf.WriteString(" ● gateway     gate\n")
		sidebarBuf.WriteString("   awaiting input\n")
		sidebarBuf.WriteString("\n")
		sidebarBuf.WriteString(" ○ architect      -\n")
		sidebarBuf.WriteString(" ○ workers        -\n")
		sidebarBuf.WriteString(" ○ qa             -")

		body = joinSplitView(contentBuf.String(), sidebarBuf.String(), contentWidth, sidebarWidth, contentHeight)
	}

	return header + body + "\n" + input + footer
}

// handleFilePickerKey processes key events while the file picker overlay is active.
func (s PromptScreen) handleFilePickerKey(msg tea.KeyPressMsg) (PromptScreen, tea.Cmd) {
	switch msg.Code {
	case tea.KeyEscape:
		s.fp.stopScan()
		s.fpActive = false
		val := s.textarea.Value()
		if s.fpAtStart < len(val) {
			s.textarea.SetValue(val[:s.fpAtStart] + val[s.fpAtStart+1+len(s.fpQuery):])
		}
		s.fpQuery = ""
		return s, nil

	case tea.KeyEnter:
		sel := s.fp.selected()
		s.fp.stopScan()
		s.fpActive = false
		if sel != "" {
			val := s.textarea.Value()
			before := val[:s.fpAtStart]
			after := ""
			end := s.fpAtStart + 1 + len(s.fpQuery)
			if end < len(val) {
				after = val[end:]
			}
			s.textarea.SetValue(before + sel + " " + after)
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
			val := s.textarea.Value()
			if s.fpAtStart < len(val) {
				s.textarea.SetValue(val[:s.fpAtStart] + val[s.fpAtStart+1:])
			}
		}
		return s, nil

	default:
		if len(msg.Text) > 0 && msg.Code != tea.KeyBackspace && msg.Code != tea.KeyEnter {
			s.fpQuery += msg.Text
			s.fp.refilter(s.fpQuery)
			var cmd tea.Cmd
			s.textarea, cmd = s.textarea.Update(msg)
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
	contentWidth := max(1, int(float64(s.width)*splitRatio))
	contentHeight := max(1, s.height-constHeaderHeight-constPromptInputHeight-constFooterHeight)
	s.fp = newFilePicker(cwd, contentWidth, contentHeight)
	s.fpActive = true
	s.fpAtStart = len(s.textarea.Value()) - 1
	s.fpQuery = ""
	scanCmd := s.fp.startScan()
	if pendingCmd != nil {
		return s, tea.Batch(pendingCmd, scanCmd)
	}
	return s, scanCmd
}
