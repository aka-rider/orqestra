//go:build darwin

package agent

import (
	"strings"
	"testing"
)

func TestBuildAgentJSON(t *testing.T) {
	t.Run("valid agent definition", func(t *testing.T) {
		def := AgentDefinition{
			Description: "Orqestra probe agent",
			Prompt:      "You are a probe. Print probe-ok and stop.",
		}
		result, err := BuildAgentJSON("orqestra-probe", def)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, `"orqestra-probe"`) {
			t.Errorf("expected agent name in JSON, got: %s", result)
		}
		if !strings.Contains(result, `"description":"Orqestra probe agent"`) {
			t.Errorf("expected description in JSON, got: %s", result)
		}
	})

	t.Run("empty name errors", func(t *testing.T) {
		_, err := BuildAgentJSON("", AgentDefinition{Prompt: "test"})
		if err == nil {
			t.Fatal("expected error for empty name")
		}
	})

	t.Run("with hooks", func(t *testing.T) {
		def := AgentDefinition{
			Description: "test",
			Prompt:      "test prompt",
			Hooks: AgentHooks{
				Stop: []AgentHookMatcher{
					{
						Hooks: []AgentHookCommand{
							{Type: "command", Command: "printf '\\a' > /dev/tty"},
						},
					},
				},
			},
		}
		result, err := BuildAgentJSON("orqestra-worker", def)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result, `"Stop"`) {
			t.Errorf("expected Stop hook in JSON, got: %s", result)
		}
		if !strings.Contains(result, `printf`) {
			t.Errorf("expected command in JSON, got: %s", result)
		}
	})
}

func TestBuildAgentMarkdown(t *testing.T) {
	t.Run("golden output", func(t *testing.T) {
		result := BuildAgentMarkdown("orqestra-probe", "Probe agent", "You are a probe.")
		expected := "---\nname: orqestra-probe\ndescription: Probe agent\n---\n\nYou are a probe.\n"
		if result != expected {
			t.Errorf("markdown mismatch:\ngot:  %q\nwant: %q", result, expected)
		}
	})

	t.Run("no description", func(t *testing.T) {
		result := BuildAgentMarkdown("test", "", "prompt text")
		if strings.Contains(result, "description:") {
			t.Errorf("should not contain description when empty, got: %s", result)
		}
	})
}

func TestBuildPTYCommandWithAgent(t *testing.T) {
	agentsJSON := `{"worker":{"description":"test","prompt":"do work"}}`

	t.Run("interactive mode", func(t *testing.T) {
		cmd := BuildPTYCommandWithAgent("", "worker", agentsJSON, true)
		assertContains(t, cmd, "--dangerously-skip-permissions")
		assertContains(t, cmd, "--agents")
		assertContains(t, cmd, "--agent")
		assertContains(t, cmd, "worker")
		// Should NOT have -p in interactive mode
		for _, arg := range cmd {
			if arg == "-p" {
				t.Error("interactive mode should not have -p flag")
			}
		}
	})

	t.Run("non-interactive mode", func(t *testing.T) {
		cmd := BuildPTYCommandWithAgent("do the task", "worker", agentsJSON, false)
		assertContains(t, cmd, "-p")
		assertContains(t, cmd, "do the task")
		assertContains(t, cmd, "--agents")
		assertContains(t, cmd, "--agent")
	})
}

func TestBuildPTYCommandWithSystemPromptFile(t *testing.T) {
	t.Run("interactive with prompt file", func(t *testing.T) {
		cmd := BuildPTYCommandWithSystemPromptFile("", "/path/to/prompt.md", true)
		assertContains(t, cmd, "--append-system-prompt-file")
		assertContains(t, cmd, "/path/to/prompt.md")
	})

	t.Run("interactive without prompt file", func(t *testing.T) {
		cmd := BuildPTYCommandWithSystemPromptFile("", "", true)
		for _, arg := range cmd {
			if arg == "--append-system-prompt-file" {
				t.Error("should not have --append-system-prompt-file when empty")
			}
		}
	})

	t.Run("non-interactive with prompt", func(t *testing.T) {
		cmd := BuildPTYCommandWithSystemPromptFile("hello", "/prompt.md", false)
		assertContains(t, cmd, "-p")
		assertContains(t, cmd, "hello")
		assertContains(t, cmd, "--append-system-prompt-file")
	})
}

func assertContains(t *testing.T, args []string, want string) {
	t.Helper()
	for _, a := range args {
		if a == want {
			return
		}
	}
	t.Errorf("args %v does not contain %q", args, want)
}
