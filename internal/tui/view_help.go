package tui

import (
	"fmt"
	"strings"
)

// renderHelp produces the help view for the active tab area.
func renderHelp(registry *CommandRegistry, state State, args string) string {
	args = strings.TrimSpace(args)

	// /help <cmd> — detailed help for a specific command
	if args != "" {
		name := args
		if !strings.HasPrefix(name, "/") {
			name = "/" + name
		}
		cmd := registry.Lookup(name)
		if cmd == nil {
			return errorStyle.Render("Unknown command: " + args)
		}
		return renderCommandDetail(cmd)
	}

	// /help — all available commands
	return renderCommandList(registry, state)
}

func renderCommandDetail(cmd *Command) string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(cmd.Name))
	b.WriteString("\n\n")
	b.WriteString(cmd.Help)
	b.WriteString("\n")

	if len(cmd.Aliases) > 0 {
		b.WriteString("\n")
		b.WriteString(subtitleStyle.Render("Aliases: "))
		b.WriteString(strings.Join(cmd.Aliases, ", "))
		b.WriteString("\n")
	}

	if cmd.DetailHelp != "" {
		b.WriteString("\n")
		b.WriteString(cmd.DetailHelp)
		b.WriteString("\n")
	}

	if len(cmd.ValidStates) > 0 {
		b.WriteString("\n")
		b.WriteString(subtitleStyle.Render("Available in: "))
		var states []string
		for _, s := range cmd.ValidStates {
			states = append(states, stateName(s))
		}
		b.WriteString(strings.Join(states, ", "))
		b.WriteString("\n")
	}

	return b.String()
}

func renderCommandList(registry *CommandRegistry, state State) string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("Available Commands"))
	b.WriteString("\n\n")

	cmds := registry.Available(state)
	if len(cmds) == 0 {
		b.WriteString(subtitleStyle.Render("No commands available in this state."))
		return b.String()
	}

	// Find max name width for alignment
	maxWidth := 0
	for _, cmd := range cmds {
		nameWithAliases := cmd.Name
		if len(cmd.Aliases) > 0 {
			nameWithAliases += " (" + strings.Join(cmd.Aliases, ", ") + ")"
		}
		if len(nameWithAliases) > maxWidth {
			maxWidth = len(nameWithAliases)
		}
	}

	for _, cmd := range cmds {
		nameWithAliases := cmd.Name
		if len(cmd.Aliases) > 0 {
			nameWithAliases += " (" + strings.Join(cmd.Aliases, ", ") + ")"
		}
		padding := strings.Repeat(" ", maxWidth-len(nameWithAliases)+2)
		line := fmt.Sprintf("  %s%s%s", goalStyle.Render(nameWithAliases), padding, cmd.Help)
		b.WriteString(line)
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(subtitleStyle.Render("Type /help <command> for detailed help."))

	return b.String()
}

// stateName returns a human-readable name for a state.
func stateName(s State) string {
	switch s {
	case StateIdle:
		return "idle"
	case StateIntentConfirm:
		return "intent-confirm"
	case StatePlanning:
		return "planning"
	case StateValidating:
		return "validating"
	case StateConfirming:
		return "confirming"
	case StateExecuting:
		return "executing"
	case StateDone:
		return "done"
	default:
		return "unknown"
	}
}
