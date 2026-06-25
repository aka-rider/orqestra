package tui

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/tui/keymap"
)

// chat is the bottom input component — the single text surface the user types
// into to steer the model. It is a real component rather than a bare textarea
// on the screen so it can own its own concerns (focus, submit) and later host
// the AskUserQuestion chip picker as a sub-component ("the prompt models
// openness") without the screen reaching into a textarea field.
//
// Value sub-model: callers hold a copy; mutating methods take a pointer.
type chat struct {
	input textarea.Model
	keys  keymap.Bindings
}

// newChat builds a focused, single-line steering input.
func newChat(keys keymap.Bindings) chat {
	ta := textarea.New()
	ta.Placeholder = "post to steer the model"
	ta.SetHeight(1)
	ta.CharLimit = 4096
	return chat{input: ta, keys: keys}
}

func (c *chat) Focus()        { c.input.Focus() }
func (c *chat) Blur()         { c.input.Blur() }
func (c *chat) Reset()        { c.input.Reset() }
func (c *chat) SetWidth(w int) { c.input.SetWidth(w) }
func (c chat) View() string   { return c.input.View() }

// Submit returns the trimmed text and clears the input, or ("", false) when the
// input is empty (nothing to send).
func (c *chat) Submit() (string, bool) {
	text := strings.TrimSpace(c.input.Value())
	if text == "" {
		return "", false
	}
	c.input.Reset()
	return text, true
}

// Update routes a message to the text input.
func (c chat) Update(msg tea.Msg) (chat, tea.Cmd) {
	var cmd tea.Cmd
	c.input, cmd = c.input.Update(msg)
	return c, cmd
}
