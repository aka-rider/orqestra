package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func testRegistry() *CommandRegistry {
	r := NewCommandRegistry()
	RegisterBuiltins(r)
	return r
}

func TestCommandBar_PlainTextEmitsPromptSubmit(t *testing.T) {
	cb := newCommandBar(testRegistry())
	cb.SetState(StateIdle)

	// Type some text
	cb.input.SetValue("build a web app")

	// Press enter
	updated, cmd := cb.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cb = updated

	if cmd == nil {
		t.Fatal("expected a command from enter on plain text")
	}

	msg := cmd()
	psm, ok := msg.(PromptSubmitMsg)
	if !ok {
		t.Fatalf("expected PromptSubmitMsg, got %T", msg)
	}
	if psm.Prompt != "build a web app" {
		t.Errorf("expected prompt 'build a web app', got %q", psm.Prompt)
	}

	// Input should be cleared
	if cb.Value() != "" {
		t.Errorf("expected input cleared after submit, got %q", cb.Value())
	}
}

func TestCommandBar_SlashCommandEmitsCommand(t *testing.T) {
	cb := newCommandBar(testRegistry())
	cb.SetState(StateIdle)

	cb.input.SetValue("/help topics")

	updated, cmd := cb.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cb = updated

	if cmd == nil {
		t.Fatal("expected a command from /help")
	}

	msg := cmd()
	cmsg, ok := msg.(CommandMsg)
	if !ok {
		t.Fatalf("expected CommandMsg, got %T", msg)
	}
	if cmsg.Name != "/help" {
		t.Errorf("expected name /help, got %q", cmsg.Name)
	}
	if cmsg.Args != "topics" {
		t.Errorf("expected args 'topics', got %q", cmsg.Args)
	}
}

func TestCommandBar_EmptyEnterDoesNothing(t *testing.T) {
	cb := newCommandBar(testRegistry())
	cb.SetState(StateIdle)

	cb.input.SetValue("")
	_, cmd := cb.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("expected no command on empty enter")
	}
}

func TestCommandBar_AutocompleteShowsOnSlash(t *testing.T) {
	cb := newCommandBar(testRegistry())
	cb.SetState(StateIdle)

	// Simulate typing /h
	cb.input.SetValue("/h")
	cb.updateAutocomplete()

	if !cb.showAC {
		t.Error("expected autocomplete to show on /h prefix")
	}
	if len(cb.suggestions) == 0 {
		t.Error("expected suggestions for /h")
	}
}

func TestCommandBar_EscDismissesAutocomplete(t *testing.T) {
	cb := newCommandBar(testRegistry())
	cb.SetState(StateIdle)

	cb.input.SetValue("/h")
	cb.updateAutocomplete()

	updated, _ := cb.Update(tea.KeyMsg{Type: tea.KeyEsc})
	cb = updated

	if cb.showAC {
		t.Error("expected autocomplete dismissed after esc")
	}
}

func TestCommandBar_HintShowsApproveReject(t *testing.T) {
	cb := newCommandBar(testRegistry())
	cb.SetState(StateConfirming)

	hint := cb.renderHint()
	if !strings.Contains(hint, "[A]") || !strings.Contains(hint, "[R]") {
		t.Errorf("expected [A] and [R] in confirming hint, got %q", hint)
	}
}
