package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/harness"
)

// questionMode is the dispatch mode for userQuestionModel.
type questionMode int

const (
	questionFreeform questionMode = iota
	questionSingle
	questionMulti
)

// userQuestionModel is a self-contained component for the AskUserQuestion
// content mode. Parent screens forward every message to Update and re-render
// after each call; rendering goes through View/Footer/InputZone.
type userQuestionModel struct {
	q            harness.MCPToolCall
	mode         questionMode
	cursor       int
	selected     map[int]bool
	custom       map[int]string
	activeEditor int // -1 = none
	ta           textarea.Model
	hasTA        bool
	width        int

	done   bool
	answer harness.MCPAnswer
}

// newUserQuestion builds a fresh component for a given question and width.
func newUserQuestion(q harness.MCPToolCall, width int) userQuestionModel {
	m := userQuestionModel{
		q:            q,
		selected:     map[int]bool{},
		custom:       map[int]string{},
		activeEditor: -1,
		width:        width,
	}
	switch {
	case len(q.Options) == 0:
		m.mode = questionFreeform
		ta := textarea.New()
		ta.Placeholder = "Type your answer..."
		ta.SetWidth(max(1, width-4))
		ta.SetHeight(3)
		ta.CharLimit = 1024
		ta.Focus()
		m.ta = ta
		m.hasTA = true
	case q.MultiSelect:
		m.mode = questionMulti
	default:
		m.mode = questionSingle
	}
	return m
}

// Init returns the cursor-blink command for the embedded textarea.
func (m userQuestionModel) Init() tea.Cmd { return textarea.Blink }

// SetWidth updates the stored width and propagates it to the active textarea.
func (m userQuestionModel) SetWidth(w int) userQuestionModel {
	m.width = w
	if !m.hasTA {
		return m
	}
	if m.activeEditor >= 0 {
		m.ta.SetWidth(max(1, w-6))
	} else {
		m.ta.SetWidth(max(1, w-4))
	}
	return m
}

// Done reports whether the component has produced a final answer.
func (m userQuestionModel) Done() bool { return m.done }

// Answer returns the final answer (only meaningful after Done()).
func (m userQuestionModel) Answer() harness.MCPAnswer { return m.answer }

// Cancel marks the question as skipped (used by parent Ctrl+C handling).
func (m userQuestionModel) Cancel() userQuestionModel {
	if m.activeEditor >= 0 {
		m.ta.Blur()
		m.activeEditor = -1
		m.hasTA = false
	}
	m.done = true
	m.answer = harness.MCPAnswer{Skipped: true}
	return m
}

// Update routes every message — key events, blink/cursor messages, anything
// the parent forwards. The textarea command is returned verbatim so the
// parent never silently drops a cursor-blink continuation.
func (m userQuestionModel) Update(msg tea.Msg) (userQuestionModel, tea.Cmd) {
	if m.done {
		return m, nil
	}
	key, isKey := msg.(tea.KeyPressMsg)
	if !isKey {
		if m.hasTA {
			var cmd tea.Cmd
			m.ta, cmd = m.ta.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	if m.activeEditor >= 0 {
		return m.updateEditor(key)
	}
	if m.mode == questionFreeform {
		return m.updateFreeform(key)
	}
	return m.updateOptions(key)
}

func (m userQuestionModel) updateEditor(key tea.KeyPressMsg) (userQuestionModel, tea.Cmd) {
	switch key.Code {
	case tea.KeyTab:
		if key.Mod == 0 {
			m.commitEditor()
			return m, nil
		}
	case tea.KeyEnter:
		if !key.Mod.Contains(tea.ModShift) && !key.Mod.Contains(tea.ModAlt) {
			m.commitEditor()
			return m, nil
		}
	case tea.KeyEscape:
		m.ta.Blur()
		m.activeEditor = -1
		m.hasTA = false
		return m, nil
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(key)
	return m, cmd
}

func (m *userQuestionModel) commitEditor() {
	text := strings.TrimSpace(m.ta.Value())
	if text != "" {
		m.custom[m.activeEditor] = text
	} else {
		delete(m.custom, m.activeEditor)
	}
	m.ta.Blur()
	m.activeEditor = -1
	m.hasTA = false
}

func (m userQuestionModel) updateFreeform(key tea.KeyPressMsg) (userQuestionModel, tea.Cmd) {
	switch key.Code {
	case tea.KeyEscape:
		m.done = true
		m.answer = harness.MCPAnswer{Skipped: true}
		return m, nil
	case tea.KeyEnter:
		if !key.Mod.Contains(tea.ModShift) && !key.Mod.Contains(tea.ModAlt) {
			text := strings.TrimSpace(m.ta.Value())
			m.done = true
			m.answer = harness.MCPAnswer{FreeformText: text}
			return m, nil
		}
		// Shift+Enter / Alt+Enter: insert newline directly. The textarea's
		// InsertNewline binding matches only the bare "enter" keystroke
		// (bubbles/v2 key.Matches is string-equality on Key.String()), so
		// forwarding "shift+enter" would be a no-op.
		m.ta.InsertString("\n")
		return m, nil
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(key)
	return m, cmd
}

func (m userQuestionModel) updateOptions(key tea.KeyPressMsg) (userQuestionModel, tea.Cmd) {
	opts := m.q.Options
	switch key.Code {
	case tea.KeyEscape:
		m.done = true
		m.answer = harness.MCPAnswer{Skipped: true}
		return m, nil
	case tea.KeyUp:
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case tea.KeyDown:
		if m.cursor < len(opts)-1 {
			m.cursor++
		}
		return m, nil
	case tea.KeyTab:
		ta := textarea.New()
		ta.Placeholder = "Add context..."
		ta.SetWidth(max(1, m.width-6))
		ta.SetHeight(1)
		ta.CharLimit = 512
		if prev, ok := m.custom[m.cursor]; ok {
			ta.SetValue(prev)
		}
		ta.Focus()
		m.ta = ta
		m.activeEditor = m.cursor
		m.hasTA = true
		return m, textarea.Blink
	case tea.KeyEnter:
		m.done = true
		m.answer = m.buildAnswer()
		return m, nil
	}
	if key.Text == " " && m.mode == questionMulti {
		m.selected[m.cursor] = !m.selected[m.cursor]
	}
	return m, nil
}

func (m userQuestionModel) buildAnswer() harness.MCPAnswer {
	var selected []int
	for i := range m.q.Options {
		if m.selected[i] {
			selected = append(selected, i)
		}
	}
	if len(selected) == 0 && m.mode == questionSingle && len(m.q.Options) > 0 {
		selected = []int{m.cursor}
	}
	var customTexts map[int]string
	for idx, text := range m.custom {
		t := strings.TrimSpace(text)
		if t == "" {
			continue
		}
		if customTexts == nil {
			customTexts = map[int]string{}
		}
		customTexts[idx] = t
	}
	return harness.MCPAnswer{SelectedIndices: selected, CustomTexts: customTexts}
}

// View renders the question header, options, inline editor, and any committed
// custom-context indicators. The freeform textarea is rendered inline here as
// well (mirrors the prompt screen's input-zone pattern).
func (m userQuestionModel) View(width int) string {
	var b strings.Builder
	b.WriteString(renderPrefixedText(phaseStyle, " ", m.q.Question, width))
	b.WriteString("\n")

	if m.mode == questionFreeform {
		b.WriteString(fmt.Sprintf("   %s\n", m.ta.View()))
		return b.String()
	}

	for i, opt := range m.q.Options {
		cursor := "  "
		if i == m.cursor {
			cursor = phaseStyle.Render("> ")
		}

		var marker string
		if m.selected[i] {
			marker = passStyle.Bold(true).Render("[x] ")
		} else {
			marker = "[ ] "
		}

		label := opt.Label
		if i == m.cursor {
			label = goalStyle.Render(opt.Label)
		}

		b.WriteString(fmt.Sprintf("%s%s%s", cursor, marker, label))
		if opt.Hint != "" {
			b.WriteString(fmt.Sprintf("  %s", questionHintStyle.Render(opt.Hint)))
		}
		if text, ok := m.custom[i]; ok && strings.TrimSpace(text) != "" && m.activeEditor != i {
			b.WriteString(fmt.Sprintf("  %s", questionHintStyle.Render("✎ "+text)))
		}
		if i == m.cursor && m.activeEditor != i {
			if text, ok := m.custom[i]; ok && strings.TrimSpace(text) != "" {
				b.WriteString("  ")
				b.WriteString(questionHintStyle.Render("[Tab: edit context]"))
			} else {
				b.WriteString("  ")
				b.WriteString(questionHintStyle.Render("[Tab: add context]"))
			}
		}
		b.WriteString("\n")

		if m.activeEditor == i {
			b.WriteString(fmt.Sprintf("     %s %s\n", questionGutterStyle.Render("┊"), m.ta.View()))
		}
	}

	return b.String()
}

// Footer returns the footer key-hint string (excluding the help / ctrl-c
// suffix that the parent appends).
func (m userQuestionModel) Footer() string {
	if m.activeEditor >= 0 {
		return " [Tab/Enter] save context | [Esc] discard"
	}
	switch m.mode {
	case questionFreeform:
		return " [Enter] submit | [Shift+Enter] newline | [Esc] skip"
	case questionMulti:
		return " [↑↓] navigate | [Space] toggle | [Tab] add context | [Enter] confirm | [Esc] skip"
	default:
		return " [↑↓] navigate | [Tab] add context | [Enter] confirm | [Esc] skip"
	}
}

// InputZone returns the short hint string for the input-zone line.
func (m userQuestionModel) InputZone() string {
	if m.activeEditor >= 0 {
		return " [Tab/Enter] save context | [Esc] discard"
	}
	switch m.mode {
	case questionFreeform:
		return " Type your answer, then [Enter] to submit | [Esc] skip"
	case questionMulti:
		return " [↑↓] navigate | [Space] toggle | [Tab] add context | [Enter] confirm | [Esc] skip"
	default:
		return " [↑↓] navigate | [Tab] add context | [Enter] confirm | [Esc] skip"
	}
}
