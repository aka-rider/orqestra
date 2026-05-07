package harness

import (
	"encoding/json"
	"fmt"
	"strings"
)

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
