//go:build darwin

package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// AgentHookCommand defines a command hook in agent frontmatter.
type AgentHookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// AgentHookMatcher defines a matcher-based hook trigger.
type AgentHookMatcher struct {
	Matcher string             `json:"matcher,omitempty"`
	Hooks   []AgentHookCommand `json:"hooks"`
}

// AgentHooks defines the lifecycle hooks for an agent.
type AgentHooks struct {
	Stop         []AgentHookMatcher `json:"Stop,omitempty"`
	Notification []AgentHookMatcher `json:"Notification,omitempty"`
}

// AgentDefinition is the JSON structure for --agents inline definition.
type AgentDefinition struct {
	Description string     `json:"description"`
	Prompt      string     `json:"prompt"`
	Hooks       AgentHooks `json:"hooks,omitempty"`
}

// BuildAgentJSON constructs the --agents JSON for an inline agent definition.
// The returned string is valid for passing to `claude --agents <json> --agent <name>`.
func BuildAgentJSON(name string, def AgentDefinition) (string, error) {
	if name == "" {
		return "", fmt.Errorf("agent name is required")
	}
	agents := map[string]AgentDefinition{name: def}
	data, err := json.Marshal(agents)
	if err != nil {
		return "", fmt.Errorf("marshal agent definition: %w", err)
	}
	return string(data), nil
}

// BuildAgentMarkdown generates the agent markdown file with YAML frontmatter.
// This is used when writing a persistent agent file to the session directory.
func BuildAgentMarkdown(name, description, prompt string) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("name: %s\n", name))
	if description != "" {
		sb.WriteString(fmt.Sprintf("description: %s\n", description))
	}
	sb.WriteString("---\n\n")
	sb.WriteString(prompt)
	sb.WriteString("\n")
	return sb.String()
}

// BuildPTYCommandWithAgent builds the Claude Code CLI command using --agents JSON.
// This is the preferred path when --agent is available.
func BuildPTYCommandWithAgent(prompt, agentName, agentsJSON string, interactive bool) []string {
	if interactive {
		cmd := []string{"claude", "--dangerously-skip-permissions", "--agents", agentsJSON, "--agent", agentName}
		return cmd
	}
	cmd := []string{"claude", "--dangerously-skip-permissions", "-p", prompt, "--agents", agentsJSON, "--agent", agentName}
	return cmd
}

// BuildPTYCommandWithSystemPromptFile builds the Claude Code CLI command using --append-system-prompt-file.
// Fallback path when --agent is unavailable or not needed.
func BuildPTYCommandWithSystemPromptFile(prompt, systemPromptFile string, interactive bool) []string {
	if interactive {
		cmd := []string{"claude", "--dangerously-skip-permissions"}
		if systemPromptFile != "" {
			cmd = append(cmd, "--append-system-prompt-file", systemPromptFile)
		}
		return cmd
	}
	cmd := []string{"claude", "--dangerously-skip-permissions", "-p", prompt}
	if systemPromptFile != "" {
		cmd = append(cmd, "--append-system-prompt-file", systemPromptFile)
	}
	return cmd
}
