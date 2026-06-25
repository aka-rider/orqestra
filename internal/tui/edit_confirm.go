package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/xiii/orqestra/internal/tui/keymap"
)

// editConfirmModel is the "apply these plan edits?" dialog shown after the user
// edits the plan in $EDITOR and the result differs from the original. It is a
// value sub-model in the shape of userQuestionModel: it owns only the dialog's
// own state — `pending` is the edited markdown it will confirm. Grouping these
// fields here (rather than flat on PipelineScreen) keeps "comment textarea open
// but no comment mode" and similar half-states from being representable.
type editConfirmModel struct {
	pending    string         // the edited plan awaiting Yes/No
	cursor     int            // 0 = Yes, 1 = No
	comment    textarea.Model // optional "describe your changes" context
	hasComment bool
}

// editConfirmResult is what an Update call resolved the dialog to.
type editConfirmResult int

const (
	editConfirmPending editConfirmResult = iota // still on the dialog
	editConfirmApply                            // user confirmed — run the edited plan
	editConfirmDiscard                          // user declined — keep the original
)

// newEditConfirm opens the dialog for an edited plan.
func newEditConfirm(pending string) editConfirmModel {
	return editConfirmModel{pending: pending}
}

// commentText returns the trimmed context comment (empty if none was added).
func (m editConfirmModel) commentText() string {
	if !m.hasComment {
		return ""
	}
	return strings.TrimSpace(m.comment.Value())
}

// Update advances the dialog. It returns the new model, whether the user
// resolved it (apply/discard) or is still deciding, and any textarea command.
// Non-key messages (cursor blink) are forwarded to the comment textarea.
func (m editConfirmModel) Update(msg tea.Msg, keys keymap.Bindings) (editConfirmModel, editConfirmResult, tea.Cmd) {
	keyMsg, isKey := msg.(tea.KeyPressMsg)
	if !isKey {
		if m.hasComment {
			var cmd tea.Cmd
			m.comment, cmd = m.comment.Update(msg)
			return m, editConfirmPending, cmd
		}
		return m, editConfirmPending, nil
	}

	if m.hasComment {
		switch {
		case key.Matches(keyMsg, keys.FocusNext): // Tab: stop adding a comment
			m.hasComment = false
			return m, editConfirmPending, nil
		case key.Matches(keyMsg, keys.Back): // Esc: discard the comment text
			m.comment.Reset()
			m.hasComment = false
			return m, editConfirmPending, nil
		case key.Matches(keyMsg, keys.Submit): // bare Enter: confirm with the comment
			return m, editConfirmApply, nil
		}
		// Shift/Alt+Enter inserts a newline; any other printable key edits the
		// comment. Newline insertion is the textarea's own concern, not a binding.
		if keyMsg.Code == tea.KeyEnter {
			m.comment.InsertString("\n")
			return m, editConfirmPending, nil
		}
		if !keyMsg.Mod.Contains(tea.ModCtrl) && !keyMsg.Mod.Contains(tea.ModAlt) && !keyMsg.Mod.Contains(tea.ModMeta) {
			var cmd tea.Cmd
			m.comment, cmd = m.comment.Update(keyMsg)
			return m, editConfirmPending, cmd
		}
		return m, editConfirmPending, nil
	}

	switch {
	case key.Matches(keyMsg, keys.Up):
		if m.cursor > 0 {
			m.cursor--
		}
	case key.Matches(keyMsg, keys.Down):
		if m.cursor < 1 {
			m.cursor++
		}
	case key.Matches(keyMsg, keys.FocusNext): // Tab: add a context comment (Yes only)
		if m.cursor == 0 {
			ta := textarea.New()
			ta.Placeholder = "Describe your changes..."
			ta.SetWidth(max(1, 80-6))
			ta.SetHeight(2)
			ta.CharLimit = 1024
			ta.Focus()
			m.comment = ta
			m.hasComment = true
		}
	case key.Matches(keyMsg, keys.Submit):
		if m.cursor == 0 {
			return m, editConfirmApply, nil
		}
		return m, editConfirmDiscard, nil
	case key.Matches(keyMsg, keys.Back):
		return m, editConfirmDiscard, nil
	}
	return m, editConfirmPending, nil
}

// View renders the Yes/No dialog and the optional context textarea.
func (m editConfirmModel) View(width int) string {
	var b strings.Builder

	b.WriteString(goalStyle.Render("  Plan was modified"))
	b.WriteString("\n\n")
	b.WriteString("  Apply these changes?\n\n")

	options := []string{"Yes, apply changes", "No, discard changes"}
	for i, opt := range options {
		cursor := "  "
		style := dimStyle
		if i == m.cursor {
			cursor = "> "
			style = phaseStyle.Bold(true)
		}
		b.WriteString(style.Render(cursor + opt))
		if i == 0 && m.cursor == 0 {
			b.WriteString(dimStyle.Render("  [Tab: add context]"))
		}
		b.WriteString("\n")
	}

	if m.hasComment {
		b.WriteString("\n")
		b.WriteString(m.comment.View())
		b.WriteString("\n")
	}

	return lipgloss.NewStyle().Width(width).Render(b.String())
}
