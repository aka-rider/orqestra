package tui

import (
	"testing"
)

func TestCommandRegistry_RegisterPanicsOnEmptyHelp(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on empty Help")
		}
	}()

	r := NewCommandRegistry()
	r.Register(Command{
		Name: "/bad",
		Help: "",
	})
}

func TestCommandRegistry_Lookup(t *testing.T) {
	r := NewCommandRegistry()
	r.Register(Command{
		Name:    "/test",
		Aliases: []string{"/t"},
		Help:    "A test command",
	})

	cmd := r.Lookup("/test")
	if cmd == nil {
		t.Fatal("expected to find /test")
	}
	if cmd.Name != "/test" {
		t.Errorf("expected name /test, got %s", cmd.Name)
	}

	cmd = r.Lookup("/t")
	if cmd == nil {
		t.Fatal("expected to find /t alias")
	}
	if cmd.Name != "/test" {
		t.Errorf("expected canonical name /test via alias, got %s", cmd.Name)
	}

	cmd = r.Lookup("/nonexistent")
	if cmd != nil {
		t.Error("expected nil for nonexistent command")
	}
}

func TestCommandRegistry_Available(t *testing.T) {
	r := NewCommandRegistry()
	r.Register(Command{
		Name: "/always",
		Help: "Always available",
	})
	r.Register(Command{
		Name:        "/idle-only",
		Help:        "Only in idle",
		ValidStates: []State{StateIdle},
	})
	r.Register(Command{
		Name:        "/plan-only",
		Help:        "Only in planning",
		ValidStates: []State{StatePlanning},
	})

	idle := r.Available(StateIdle)
	if len(idle) != 2 {
		t.Errorf("expected 2 commands in StateIdle, got %d", len(idle))
	}

	planning := r.Available(StatePlanning)
	if len(planning) != 2 {
		t.Errorf("expected 2 commands in StatePlanning, got %d", len(planning))
	}

	// StateConfirming should only have the always-available one
	confirming := r.Available(StateConfirming)
	if len(confirming) != 1 {
		t.Errorf("expected 1 command in StateConfirming, got %d", len(confirming))
	}
}

func TestCommandRegistry_Complete(t *testing.T) {
	r := NewCommandRegistry()
	r.Register(Command{
		Name:    "/help",
		Aliases: []string{"/h"},
		Help:    "Show help",
	})
	r.Register(Command{
		Name: "/hello",
		Help: "Say hello",
	})
	r.Register(Command{
		Name:        "/hidden",
		Help:        "Hidden in idle",
		ValidStates: []State{StatePlanning},
	})

	// /h prefix should match /help and /hello (via name) in StateIdle
	results := r.Complete("/h", StateIdle)
	if len(results) != 2 {
		t.Errorf("expected 2 completions for /h in idle, got %d", len(results))
	}

	// /he prefix in planning state should include all three
	results = r.Complete("/he", StatePlanning)
	if len(results) != 2 {
		t.Errorf("expected 2 completions for /he in planning, got %d", len(results))
	}

	// /hi should match /hidden only in StatePlanning
	results = r.Complete("/hi", StatePlanning)
	if len(results) != 1 {
		t.Errorf("expected 1 completion for /hi in planning, got %d", len(results))
	}

	results = r.Complete("/hi", StateIdle)
	if len(results) != 0 {
		t.Errorf("expected 0 completions for /hi in idle, got %d", len(results))
	}
}

func TestCommandRegistry_Builtins(t *testing.T) {
	r := NewCommandRegistry()
	RegisterBuiltins(r)

	// Verify all expected builtins exist
	expected := []string{"/help", "/logs", "/status", "/quit", "/clear", "/abort"}
	for _, name := range expected {
		if r.Lookup(name) == nil {
			t.Errorf("expected builtin %s to be registered", name)
		}
	}

	// Verify aliases
	aliases := map[string]string{
		"/h":    "/help",
		"/?":    "/help",
		"/log":  "/logs",
		"/s":    "/status",
		"/q":    "/quit",
		"/exit": "/quit",
	}
	for alias, canonical := range aliases {
		cmd := r.Lookup(alias)
		if cmd == nil {
			t.Errorf("expected alias %s to resolve", alias)
			continue
		}
		if cmd.Name != canonical {
			t.Errorf("alias %s: expected %s, got %s", alias, canonical, cmd.Name)
		}
	}
}
