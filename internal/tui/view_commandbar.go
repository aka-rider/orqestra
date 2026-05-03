package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// commandBarModel is the persistent command bar at the bottom of the TUI.
type commandBarModel struct {
	input       textinput.Model
	registry    *CommandRegistry
	state       State // current model state for autocomplete filtering
	suggestions []Command
	showAC      bool // autocomplete overlay visible
	acIndex     int  // selected autocomplete index
	focused     bool
	width       int
}

func newCommandBar(registry *CommandRegistry) commandBarModel {
	ti := textinput.New()
	ti.Placeholder = "type a prompt or /command..."
	ti.Focus()
	ti.CharLimit = 2000
	ti.Width = 80

	return commandBarModel{
		input:    ti,
		registry: registry,
	}
}

// SetWidth adjusts the input width to fill available space.
func (c *commandBarModel) SetWidth(w int) {
	c.width = w
	if w > 4 {
		c.input.Width = w - 4
	}
}

// SetState updates the current state for filtering commands.
func (c *commandBarModel) SetState(state State) {
	c.state = state
}

// Focus gives keyboard focus to the command bar.
func (c *commandBarModel) Focus() {
	c.input.Focus()
}

// Blur removes keyboard focus from the command bar.
func (c *commandBarModel) Blur() {
	c.input.Blur()
}

// Value returns the current input text.
func (c commandBarModel) Value() string {
	return c.input.Value()
}

// Reset clears the input.
func (c *commandBarModel) Reset() {
	c.input.SetValue("")
	c.showAC = false
	c.suggestions = nil
	c.acIndex = 0
}

func (c commandBarModel) Update(msg tea.Msg) (commandBarModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			value := strings.TrimSpace(c.input.Value())
			if value == "" {
				return c, nil
			}

			// If autocomplete is showing and an item is selected, use it
			if c.showAC && len(c.suggestions) > 0 {
				selected := c.suggestions[c.acIndex]
				c.input.SetValue(selected.Name + " ")
				c.showAC = false
				c.suggestions = nil
				c.acIndex = 0
				return c, nil
			}

			c.input.SetValue("")
			c.showAC = false
			c.suggestions = nil
			c.acIndex = 0

			if strings.HasPrefix(value, "/") {
				// Parse command
				parts := strings.SplitN(value, " ", 2)
				name := parts[0]
				args := ""
				if len(parts) > 1 {
					args = parts[1]
				}
				cmd := c.registry.Lookup(name)
				if cmd != nil && cmd.isAvailable(c.state) && cmd.Run != nil {
					return c, cmd.Run(args)
				}
				// Unknown command — emit as CommandMsg anyway for model to handle
				return c, func() tea.Msg {
					return CommandMsg{Name: name, Args: args}
				}
			}

			// Plain text → prompt submit
			return c, func() tea.Msg {
				return PromptSubmitMsg{Prompt: value}
			}

		case "tab":
			if c.showAC && len(c.suggestions) > 0 {
				selected := c.suggestions[c.acIndex]
				c.input.SetValue(selected.Name + " ")
				c.input.SetCursor(len(selected.Name) + 1)
				c.showAC = false
				c.suggestions = nil
				c.acIndex = 0
				return c, nil
			}
			return c, nil

		case "up":
			if c.showAC && len(c.suggestions) > 0 {
				c.acIndex = (c.acIndex - 1 + len(c.suggestions)) % len(c.suggestions)
				return c, nil
			}

		case "down":
			if c.showAC && len(c.suggestions) > 0 {
				c.acIndex = (c.acIndex + 1) % len(c.suggestions)
				return c, nil
			}

		case "shift+tab":
			if c.showAC && len(c.suggestions) > 0 {
				c.acIndex = (c.acIndex - 1 + len(c.suggestions)) % len(c.suggestions)
				return c, nil
			}
			return c, nil

		case "esc":
			if c.showAC {
				c.showAC = false
				c.suggestions = nil
				c.acIndex = 0
				return c, nil
			}
			return c, nil
		}
	}

	// Forward to textinput
	var cmd tea.Cmd
	c.input, cmd = c.input.Update(msg)

	// Update autocomplete state
	c.updateAutocomplete()

	return c, cmd
}

// updateAutocomplete refreshes suggestions based on current input.
func (c *commandBarModel) updateAutocomplete() {
	value := c.input.Value()
	if strings.HasPrefix(value, "/") && !strings.Contains(value, " ") {
		c.suggestions = c.registry.Complete(value, c.state)
		c.showAC = len(c.suggestions) > 0
		if c.acIndex >= len(c.suggestions) {
			c.acIndex = 0
		}
	} else {
		c.showAC = false
		c.suggestions = nil
		c.acIndex = 0
	}
}

// View renders the command bar (input + hint line).
func (c commandBarModel) View() string {
	inputLine := "> " + c.input.View()
	hint := c.renderHint()
	content := lipgloss.JoinVertical(lipgloss.Left, inputLine, hint)

	border := InputBoxStyle
	if c.focused {
		border = InputBoxFocusedStyle
	}
	return border.Width(c.width).Render(content)
}

// ViewAutocomplete renders the autocomplete overlay above the command bar.
func (c commandBarModel) ViewAutocomplete() string {
	if !c.showAC || len(c.suggestions) == 0 {
		return ""
	}

	var lines []string
	for i, cmd := range c.suggestions {
		name := cmd.Name
		help := cmd.Help
		line := name + "  " + help
		if i == c.acIndex {
			lines = append(lines, acSelectedStyle.Render(line))
		} else {
			lines = append(lines, acItemStyle.Render(line))
		}
	}

	return acOverlayStyle.Render(strings.Join(lines, "\n"))
}

// renderHint produces the contextual hint line.
func (c commandBarModel) renderHint() string {
	switch c.state {
	case StateConfirming, StateIntentConfirm:
		approve := approveKeyStyle.Render("[A]")
		reject := rejectKeyStyle.Render("[R]")
		return commandBarHintStyle.Render("  " + approve + "pprove or " + reject + "eject?")
	default:
		cmds := c.registry.Available(c.state)
		var names []string
		for _, cmd := range cmds {
			if len(names) >= 5 {
				break
			}
			names = append(names, cmd.Name)
		}
		return commandBarHintStyle.Render("  " + strings.Join(names, "  "))
	}
}
