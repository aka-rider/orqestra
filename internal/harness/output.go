package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// normalizeArgs returns compact JSON for tool input, used as a loop fingerprint.
// Returns empty string if input is nil or malformed.
func normalizeArgs(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, input); err != nil {
		return ""
	}
	return buf.String()
}

// ToolDetail extracts a human-readable summary from a tool invocation's
// name and raw JSON arguments. Used to populate the TUI activity bar.
func ToolDetail(name string, args json.RawMessage) string {
	// MCP tools: mcp__server__tool → "server/tool"
	if strings.HasPrefix(name, "mcp__") {
		parts := strings.SplitN(name, "__", 3)
		if len(parts) == 3 {
			return parts[1] + "/" + parts[2]
		}
		return name
	}

	if len(args) == 0 {
		return ""
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(args, &fields); err != nil {
		return ""
	}

	switch name {
	case "Read", "Write", "MultiEdit":
		return extractString(fields, "file_path")
	case "Bash":
		cmd := extractString(fields, "command")
		if len(cmd) > 60 {
			return cmd[:60] + "…"
		}
		return cmd
	case "Grep", "Glob":
		return extractString(fields, "pattern")
	case "TodoRead", "TodoWrite":
		return extractString(fields, "file_path")
	default:
		return ""
	}
}

// extractString pulls a string value from a map of raw JSON fields.
func extractString(fields map[string]json.RawMessage, key string) string {
	raw, ok := fields[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// ParseLLMOutput extracts structured JSON from Claude CLI output into target.
// Handles: raw JSON, markdown code-fenced JSON, and Claude result envelopes.
func ParseLLMOutput(raw string, target any) error {
	content := strings.TrimSpace(raw)

	// Strip markdown code fences if present.
	content = stripOutputFences(content)

	// Unwrap Claude envelope: {"type":"result","result":"..."}
	var envelope struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err == nil && envelope.Result != "" {
		content = stripOutputFences(strings.TrimSpace(envelope.Result))
	}

	if err := json.Unmarshal([]byte(content), target); err != nil {
		return fmt.Errorf("parse LLM output: %w (raw length: %d)", err, len(raw))
	}
	return nil
}

// stripOutputFences removes ```json ... ``` wrapping from a string.
func stripOutputFences(s string) string {
	if strings.HasPrefix(s, "```") {
		idx := strings.Index(s, "\n")
		if idx == -1 {
			return s
		}
		s = s[idx+1:]
		if last := strings.LastIndex(s, "```"); last >= 0 {
			s = s[:last]
		}
		return strings.TrimSpace(s)
	}

	// Look for code fence anywhere.
	for _, prefix := range []string{"```json", "```JSON", "```\n{"} {
		fenceStart := strings.Index(s, prefix)
		if fenceStart >= 0 {
			rest := s[fenceStart:]
			idx := strings.Index(rest, "\n")
			if idx >= 0 {
				rest = rest[idx+1:]
				if last := strings.LastIndex(rest, "```"); last >= 0 {
					rest = rest[:last]
				}
				return strings.TrimSpace(rest)
			}
		}
	}

	// Extract raw JSON object.
	first := strings.Index(s, "{")
	last := strings.LastIndex(s, "}")
	if first >= 0 && last > first {
		return s[first : last+1]
	}
	return s
}
