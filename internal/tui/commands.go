package tui

import (
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Command represents a registered slash command.
type Command struct {
	Name        string
	Aliases     []string
	Help        string // required, panics if empty on Register
	DetailHelp  string
	ValidStates []State // empty = always available
	Run         func(args string) tea.Cmd
}

// isAvailable returns true if the command is available in the given state.
func (c *Command) isAvailable(state State) bool {
	if len(c.ValidStates) == 0 {
		return true
	}
	for _, s := range c.ValidStates {
		if s == state {
			return true
		}
	}
	return false
}

// CommandRegistry holds all registered commands.
type CommandRegistry struct {
	commands map[string]*Command // name → command
	aliases  map[string]string   // alias → canonical name
	order    []string            // insertion order for display
}

// NewCommandRegistry creates a new empty command registry.
func NewCommandRegistry() *CommandRegistry {
	return &CommandRegistry{
		commands: make(map[string]*Command),
		aliases:  make(map[string]string),
	}
}

// Register adds a command to the registry. Panics if Help is empty.
func (r *CommandRegistry) Register(cmd Command) {
	if cmd.Help == "" {
		panic("command " + cmd.Name + " has empty Help")
	}
	r.commands[cmd.Name] = &cmd
	r.order = append(r.order, cmd.Name)
	for _, alias := range cmd.Aliases {
		r.aliases[alias] = cmd.Name
	}
}

// Lookup finds a command by name or alias. Returns nil if not found.
func (r *CommandRegistry) Lookup(name string) *Command {
	if cmd, ok := r.commands[name]; ok {
		return cmd
	}
	if canonical, ok := r.aliases[name]; ok {
		return r.commands[canonical]
	}
	return nil
}

// Available returns commands available in the given state, in registration order.
func (r *CommandRegistry) Available(state State) []Command {
	var result []Command
	for _, name := range r.order {
		cmd := r.commands[name]
		if cmd.isAvailable(state) {
			result = append(result, *cmd)
		}
	}
	return result
}

// Complete returns commands matching the given prefix that are valid in the current state.
func (r *CommandRegistry) Complete(prefix string, state State) []Command {
	prefix = strings.ToLower(prefix)
	var result []Command

	// Collect all names and aliases
	type entry struct {
		name string
		cmd  *Command
	}
	var entries []entry
	for _, name := range r.order {
		cmd := r.commands[name]
		entries = append(entries, entry{name: name, cmd: cmd})
		for _, alias := range cmd.Aliases {
			entries = append(entries, entry{name: alias, cmd: cmd})
		}
	}

	// Sort for consistent results
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].name < entries[j].name
	})

	seen := make(map[string]bool)
	for _, e := range entries {
		if strings.HasPrefix(strings.ToLower(e.name), prefix) && e.cmd.isAvailable(state) {
			if !seen[e.cmd.Name] {
				seen[e.cmd.Name] = true
				result = append(result, *e.cmd)
			}
		}
	}
	return result
}

// RegisterBuiltins adds the default set of commands.
func RegisterBuiltins(r *CommandRegistry) {
	r.Register(Command{
		Name:    "/help",
		Aliases: []string{"/h", "/?"},
		Help:    "Show available commands",
		DetailHelp: `Usage: /help [command]

With no arguments, shows all commands available in the current state.
With a command name, shows detailed help for that command.`,
		Run: func(args string) tea.Cmd {
			return func() tea.Msg { return CommandMsg{Name: "/help", Args: args} }
		},
	})

	r.Register(Command{
		Name:    "/logs",
		Aliases: []string{"/log"},
		Help:    "Toggle log panel",
		DetailHelp: `Usage: /logs

Toggles the visibility of the log panel at the bottom of the screen.
Logs show structured output from planner, validator, and worker processes.`,
		Run: func(_ string) tea.Cmd {
			return func() tea.Msg { return ToggleLogsMsg{} }
		},
	})

	r.Register(Command{
		Name:    "/status",
		Aliases: []string{"/s"},
		Help:    "Show pipeline state",
		DetailHelp: `Usage: /status

Shows the current state of the pipeline: idle, planning, validating, 
confirming, executing, or done.`,
		Run: func(_ string) tea.Cmd {
			return func() tea.Msg { return CommandMsg{Name: "/status", Args: ""} }
		},
	})

	r.Register(Command{
		Name:    "/quit",
		Aliases: []string{"/q", "/exit"},
		Help:    "Exit orqestra",
		Run: func(_ string) tea.Cmd {
			return tea.Quit
		},
	})

	r.Register(Command{
		Name:        "/clear",
		Help:        "Clear output",
		ValidStates: []State{StateIdle},
		DetailHelp:  `Usage: /clear\n\nClears the current tab output. Only available when idle.`,
		Run: func(_ string) tea.Cmd {
			return func() tea.Msg { return CommandMsg{Name: "/clear", Args: ""} }
		},
	})

	r.Register(Command{
		Name:        "/abort",
		Help:        "Cancel current operation",
		ValidStates: []State{StatePlanning, StateExecuting},
		DetailHelp:  `Usage: /abort\n\nCancels the currently running planning or execution operation.`,
		Run: func(_ string) tea.Cmd {
			return func() tea.Msg { return CommandMsg{Name: "/abort", Args: ""} }
		},
	})
}
