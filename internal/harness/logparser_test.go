package harness

import (
	"strings"
	"testing"
)

func TestParseSessionLogStream_ExtractsActivitiesAndText(t *testing.T) {
	// Simulate a JSONL session log with assistant text and tool use events
	logLines := []string{
		`{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello world\n"}}`,
		`{"type":"content_block_start","content_block":{"type":"tool_use","name":"Read","input":{"file_path":"go.mod"}}}`,
		`{"type":"content_block_delta","delta":{"type":"text_delta","text":"More output\n"}}`,
		`{"type":"result","subtype":"success","result":"done","usage":{"input_tokens":100,"output_tokens":50}}`,
	}
	input := strings.Join(logLines, "\n")

	updates, err := ParseSessionLogStream(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var activities []StreamUpdate
	var lines []string
	for _, u := range updates {
		if u.Tool != "" {
			activities = append(activities, u)
		}
		if u.Text != "" {
			lines = append(lines, strings.TrimSuffix(u.Text, "\n"))
		}
	}

	if len(activities) != 1 {
		t.Fatalf("activities = %d, want 1", len(activities))
	}
	if activities[0].Tool != "Read" {
		t.Errorf("activities[0].Tool = %q, want Read", activities[0].Tool)
	}
	if activities[0].Detail != "go.mod" {
		t.Errorf("activities[0].Detail = %q, want go.mod", activities[0].Detail)
	}

	if len(lines) != 2 {
		t.Fatalf("lines = %v, want 2 lines", lines)
	}
	if lines[0] != "Hello world" {
		t.Errorf("lines[0] = %q, want 'Hello world'", lines[0])
	}
	if lines[1] != "More output" {
		t.Errorf("lines[1] = %q, want 'More output'", lines[1])
	}
}

func TestParseSessionLogStream_EmptyInput(t *testing.T) {
	updates, err := ParseSessionLogStream(strings.NewReader(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(updates) != 0 {
		t.Errorf("updates = %d, want 0", len(updates))
	}
}

func TestParseSessionLogStream_InvalidJSON(t *testing.T) {
	input := "not json\n{also bad\n"
	updates, err := ParseSessionLogStream(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(updates) != 0 {
		t.Errorf("updates = %d, want 0", len(updates))
	}
}

func TestParseSessionLogStream_AssistantMessage(t *testing.T) {
	// Full assistant message with tool_use content blocks
	logLine := `{"type":"assistant","message":{"content":[{"type":"text","text":"Analyzing...\n"},{"type":"tool_use","name":"Bash","input":{"command":"ls -la"}}]}}`
	updates, err := ParseSessionLogStream(strings.NewReader(logLine))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var activities []StreamUpdate
	var lines []string
	for _, u := range updates {
		if u.Tool != "" {
			activities = append(activities, u)
		}
		if u.Text != "" {
			lines = append(lines, strings.TrimSuffix(u.Text, "\n"))
		}
	}
	if len(activities) != 1 || activities[0].Tool != "Bash" {
		t.Errorf("activities = %+v, want [{Bash, ls -la}]", activities)
	}
	if len(lines) != 1 || lines[0] != "Analyzing..." {
		t.Errorf("lines = %v, want ['Analyzing...']", lines)
	}
}
