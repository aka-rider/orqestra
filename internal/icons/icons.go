package icons

import "strings"

const (
	Pass    = "✓"
	Fail    = "✕"
	Warning = "⚠"
	Read    = "✑"
	Write   = "✎"
	Search  = "⚲"
	Execute = "❯"
	Tool    = "⚒"
	Unknown = "·"
)

// ForAction maps a tool or action name to an aesthetic icon symbol.
func ForAction(toolName string) string {
	switch toolName {
	case "Read", "TodoRead":
		return Read
	case "Write", "MultiEdit", "TodoWrite":
		return Write
	case "Bash":
		return Execute
	case "Grep", "Glob":
		return Search
	default:
		if strings.HasPrefix(toolName, "mcp__") {
			return Tool
		}
		return Unknown
	}
}
