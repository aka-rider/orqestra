package tui

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/mcp"
	"github.com/xiii/orqestra/internal/tui/keymap"
)

// chat is the bottom input component — the single text surface the user types
// into to steer the model. It is a real component rather than a bare textarea
// on the screen so it can model its own openness: when the model asks an
// AskUserQuestion, the chat hosts that question (its options/answer) as a
// sub-component instead of a separate screen mode. The text input and the
// question are mutually exclusive — `question` is the chat's open state.
//
// Value sub-model: callers hold a copy; mutating methods take a pointer.
type chat struct {
	input textarea.Model
	keys  keymap.Bindings
	width int

	// question is the open AskUserQuestion; questionOpen gates routing/rendering.
	question     userQuestionModel
	questionOpen bool
}

// newChat builds a focused, single-line steering input.
func newChat(keys keymap.Bindings) chat {
	ta := textarea.New()
	ta.Placeholder = "post to steer the model"
	ta.SetHeight(1)
	ta.CharLimit = 4096
	return chat{input: ta, keys: keys, question: userQuestionModel{activeEditor: -1}}
}

func (c *chat) Focus() { c.input.Focus() }
func (c *chat) Blur()  { c.input.Blur() }
func (c *chat) Reset() {
	c.input.Reset()
	c.CloseQuestion()
}

func (c *chat) SetWidth(w int) {
	c.width = w
	c.input.SetWidth(w)
	if c.questionOpen {
		c.question = c.question.SetWidth(w)
	}
}

// View renders the open question when there is one, otherwise the text input.
func (c chat) View() string {
	if c.questionOpen {
		return c.question.View(c.width)
	}
	return c.input.View()
}

// OpenQuestion surfaces a model question as the chat's open state.
func (c *chat) OpenQuestion(q mcp.ToolCall, width int) {
	c.width = width
	c.question = newUserQuestion(q, width)
	c.questionOpen = true
}

// CloseQuestion returns the chat to plain text input.
func (c *chat) CloseQuestion() {
	c.questionOpen = false
	c.question = userQuestionModel{activeEditor: -1}
}

// QuestionOpen reports whether a question is awaiting an answer.
func (c chat) QuestionOpen() bool { return c.questionOpen }

// Submit returns the trimmed text and clears the input, or ("", false) when the
// input is empty (nothing to send). Never called while a question is open.
func (c *chat) Submit() (string, bool) {
	text := strings.TrimSpace(c.input.Value())
	if text == "" {
		return "", false
	}
	c.input.Reset()
	return text, true
}

// Update routes a message to the open question or, failing that, the text input.
func (c chat) Update(msg tea.Msg) (chat, tea.Cmd) {
	if c.questionOpen {
		var cmd tea.Cmd
		c.question, cmd = c.question.Update(msg)
		return c, cmd
	}
	var cmd tea.Cmd
	c.input, cmd = c.input.Update(msg)
	return c, cmd
}
