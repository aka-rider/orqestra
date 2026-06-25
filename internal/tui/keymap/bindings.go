// Package keymap is the single source of truth for Orqestra's TUI key bindings.
//
// It is a leaf package: it imports nothing from internal/tui. Every key the app
// reacts to is declared once as a field on Bindings; call sites dispatch with
// key.Matches(msg, keys.X) and NEVER with msg.String()=="enter" in the wild.
// One physical key resolves to exactly one logical action — enforced at startup
// by ValidateNoPhysicalKeyCollisions.
//
// Rule (non-printable globals): every binding here is a control/navigation key
// (ctrl+…, arrows, pgup/pgdn, home/end, enter, esc, tab). No printable rune is
// ever bound — printable input always belongs to the focused Chat. Text-editing
// micro-keys inside an input widget (newline, chip backspace, "@" mention) are
// the input's own concern and are deliberately NOT modelled here.
package keymap

import (
	"fmt"
	"reflect"

	"charm.land/bubbles/v2/key"
)

// Bindings is the complete, declarative key map for the TUI. Fields are grouped
// by concern; the order is also the order help entries are emitted in.
type Bindings struct {
	// Navigation (lists, menus, option pickers).
	Up    key.Binding
	Down  key.Binding
	Left  key.Binding
	Right key.Binding

	// Scrolling a transcript / viewport.
	PageUp       key.Binding
	PageDown     key.Binding
	ScrollTop    key.Binding
	ScrollBottom key.Binding

	// Submission and focus movement.
	Submit    key.Binding // enter — send chat / select / confirm
	Back      key.Binding // esc — back / cancel a sub-view
	FocusNext key.Binding // tab
	FocusPrev key.Binding // shift+tab

	// Process lifetime.
	Cancel key.Binding // ctrl+c — cancel the running flow (two-tap to quit)
	Quit   key.Binding // ctrl+q — quit outright

	// Run management.
	NewRun     key.Binding // ctrl+n
	RunsList   key.Binding // ctrl+r
	RestartRun key.Binding // ctrl+shift+r

	// Transcript / plan actions.
	ExpandTools      key.Binding // ctrl+o — expand/collapse the tool frames
	ApprovePlan      key.Binding // ctrl+a — hard gate: approve and resume the flow
	OpenPlanInEditor key.Binding // ctrl+e — open the plan in $EDITOR
	OpenStepLog      key.Binding // ctrl+l — open a run-detail step log

	// Panels.
	SetupPanel key.Binding // ctrl+p

	// Clipboard.
	Copy          key.Binding // super+c — copy selection (or hovered frame)
	CopySelection key.Binding // super+shift+c — copy the active selection
}

// Default returns the shipped key map. It MUST pass ValidateNoPhysicalKeyCollisions.
func Default() Bindings {
	return Bindings{
		Up:    key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "up")),
		Down:  key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "down")),
		Left:  key.NewBinding(key.WithKeys("left"), key.WithHelp("←", "left")),
		Right: key.NewBinding(key.WithKeys("right"), key.WithHelp("→", "right")),

		PageUp:       key.NewBinding(key.WithKeys("pgup", "ctrl+b"), key.WithHelp("pgup", "page up")),
		PageDown:     key.NewBinding(key.WithKeys("pgdown", "ctrl+f"), key.WithHelp("pgdn", "page down")),
		ScrollTop:    key.NewBinding(key.WithKeys("home", "ctrl+home"), key.WithHelp("home", "top")),
		ScrollBottom: key.NewBinding(key.WithKeys("end", "ctrl+end"), key.WithHelp("end", "bottom")),

		Submit:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "send")),
		Back:      key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		FocusNext: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next")),
		FocusPrev: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("⇧tab", "prev")),

		Cancel: key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("^c", "cancel")),
		Quit:   key.NewBinding(key.WithKeys("ctrl+q"), key.WithHelp("^q", "quit")),

		NewRun:     key.NewBinding(key.WithKeys("ctrl+n"), key.WithHelp("^n", "new run")),
		RunsList:   key.NewBinding(key.WithKeys("ctrl+r"), key.WithHelp("^r", "runs")),
		RestartRun: key.NewBinding(key.WithKeys("ctrl+shift+r"), key.WithHelp("^⇧r", "restart")),

		ExpandTools:      key.NewBinding(key.WithKeys("ctrl+o"), key.WithHelp("^o", "expand tools")),
		ApprovePlan:      key.NewBinding(key.WithKeys("ctrl+a"), key.WithHelp("^a", "approve")),
		OpenPlanInEditor: key.NewBinding(key.WithKeys("ctrl+e"), key.WithHelp("^e", "edit plan")),
		OpenStepLog:      key.NewBinding(key.WithKeys("ctrl+l"), key.WithHelp("^l", "step log")),

		SetupPanel: key.NewBinding(key.WithKeys("ctrl+p"), key.WithHelp("^p", "setup")),

		Copy:          key.NewBinding(key.WithKeys("super+c"), key.WithHelp("⌘c", "copy")),
		CopySelection: key.NewBinding(key.WithKeys("super+shift+c"), key.WithHelp("⌘⇧c", "copy selection")),
	}
}

// HelpEntry is one key+description pair for the footer/help surface.
type HelpEntry struct {
	Key  string
	Desc string
}

// eachBinding visits every key.Binding field in declaration order. It is the
// single enumeration point: AllPhysicalKeys, AllHelp, and the collision
// validator all build on it, so a newly added field is covered automatically
// with no parallel list to maintain.
func (b Bindings) eachBinding(fn func(key.Binding)) {
	v := reflect.ValueOf(b)
	for i := 0; i < v.NumField(); i++ {
		if kb, ok := v.Field(i).Interface().(key.Binding); ok {
			fn(kb)
		}
	}
}

// AllHelp returns a key+description entry for every binding that has help text,
// in declaration order. Reflecting over the struct guarantees the footer help
// can never drift from the actual bindings.
func (b Bindings) AllHelp() []HelpEntry {
	var entries []HelpEntry
	b.eachBinding(func(kb key.Binding) {
		h := kb.Help()
		if h.Key == "" {
			return
		}
		entries = append(entries, HelpEntry{Key: h.Key, Desc: h.Desc})
	})
	return entries
}

// AllPhysicalKeys returns every physical key string bound anywhere in the map.
func (b Bindings) AllPhysicalKeys() []string {
	var keys []string
	b.eachBinding(func(kb key.Binding) {
		keys = append(keys, kb.Keys()...)
	})
	return keys
}

// ValidateNoPhysicalKeyCollisions fails if any physical key is bound to more
// than one action. Call it once at startup; a duplicate is a programming error,
// not a user condition.
func (b Bindings) ValidateNoPhysicalKeyCollisions() error {
	seen := make(map[string]bool)
	for _, k := range b.AllPhysicalKeys() {
		if seen[k] {
			return fmt.Errorf("keymap: physical key %q is bound to more than one action", k)
		}
		seen[k] = true
	}
	return nil
}
