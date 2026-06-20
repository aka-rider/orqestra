package tui

import (
	"fmt"

	"rune/pkg/command"
	"rune/pkg/editor/keybind"
	"rune/pkg/terminal"
	"rune/pkg/ui/components/textedit"
	"rune/pkg/ui/keymap"
	"rune/pkg/ui/styles"
)

// runeUI holds the shared rune setup bundle built once at startup.
// All rune types are confined to runeui.go, promptinput.go, and planview.go —
// no other TUI file names a rune type.
type runeUI struct {
	keys     keymap.Bindings
	styles   styles.Styles
	registry command.Registry
	resolver keybind.Resolver
	caps     terminal.TermCaps
}

// newRuneUI replicates app.go:21-68: build keymap, register textedit commands,
// validate, build resolver, detect terminal capabilities. Fails closed.
func newRuneUI() (runeUI, error) {
	keys := keymap.Default()
	st := styles.Default()

	builder := command.NewBuilder()
	builder, err := textedit.RegisterCommands(builder)
	if err != nil {
		return runeUI{}, fmt.Errorf("registering textedit commands: %w", err)
	}
	registry := builder.Build()

	if err := keys.ValidateNoPhysicalKeyCollisions(); err != nil {
		return runeUI{}, fmt.Errorf("keymap validation: %w", err)
	}

	cmdBindings, err := keys.CommandBindings()
	if err != nil {
		return runeUI{}, fmt.Errorf("building command bindings: %w", err)
	}

	for i, b := range cmdBindings {
		cmd, ok := registry.Get(b.Command)
		if !ok {
			return runeUI{}, fmt.Errorf("binding references unknown command %q", b.Command)
		}
		if b.When != "" && b.When != cmd.When {
			return runeUI{}, fmt.Errorf("binding %q predicate %q does not match command predicate %q",
				b.Command, b.When, cmd.When)
		}
		if b.When == "" {
			cmdBindings[i].When = cmd.When
		}
	}

	resolver, err := keybind.NewResolver(cmdBindings)
	if err != nil {
		return runeUI{}, fmt.Errorf("building keybind resolver: %w", err)
	}

	caps := terminal.DetectCapabilities()

	return runeUI{
		keys:     keys,
		styles:   st,
		registry: registry,
		resolver: resolver,
		caps:     caps,
	}, nil
}
