package harness

import (
	"os"
	"path/filepath"
	"testing"
)

func parseLogFileUpdates(t *testing.T, path string, maxLines int) []Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open log file: %v", err)
	}
	defer f.Close()

	updates, err := ParseSessionLogStream(f)
	if err != nil {
		t.Fatalf("ParseSessionLogStream: %v", err)
	}
	if maxLines > 0 && len(updates) > maxLines {
		updates = updates[len(updates)-maxLines:]
	}
	return updates
}

func TestCwdToDash(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/Users/foo/bar", "-Users-foo-bar"},
		{"/", "-"},
		{"/Users/foo/bar/", "-Users-foo-bar-"},
		{"/home/user/project", "-home-user-project"},
	}
	for _, tt := range tests {
		got := CwdToDash(tt.input)
		if got != tt.want {
			t.Errorf("CwdToDash(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseSessionLogStream_File_ToolUseAndText(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "test.jsonl")

	// Write sample JSONL with assistant messages
	lines := `{"type":"user","message":{"content":[{"type":"text","text":"do something"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"go.mod"}},{"type":"text","text":"I found the module definition."}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./internal/config/..."}}]}}
{"type":"system","message":{"content":[{"type":"text","text":"system message"}]}}
`
	if err := os.WriteFile(logPath, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	updates := parseLogFileUpdates(t, logPath, 100)

	var entries []Event
	for _, u := range updates {
		if u.Tool != "" || u.Text != "" {
			entries = append(entries, u)
		}
	}

	// Should have 3 entries: Read tool_use, text, Bash tool_use
	// user and system lines are skipped
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	var sawRead, sawText, sawBash bool
	for _, entry := range entries {
		if entry.Tool == "Read" && entry.Detail == "go.mod" {
			sawRead = true
		}
		if entry.Text == "I found the module definition." {
			sawText = true
		}
		if entry.Tool == "Bash" {
			sawBash = true
		}
	}
	if !sawRead {
		t.Error("missing tool_use Read/go.mod entry")
	}
	if !sawText {
		t.Error("missing assistant text entry")
	}
	if !sawBash {
		t.Error("missing tool_use Bash entry")
	}
}

func TestParseSessionLogStream_File_MaxLines(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "test.jsonl")

	// Write 5 assistant messages with one tool_use each
	var content string
	for i := 0; i < 5; i++ {
		content += `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"file.go"}}]}}` + "\n"
	}
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	entries := parseLogFileUpdates(t, logPath, 3)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries (maxLines), got %d", len(entries))
	}
}

func TestParseSessionLogStream_File_EmptyFile(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "empty.jsonl")
	os.WriteFile(logPath, []byte(""), 0o644)

	entries := parseLogFileUpdates(t, logPath, 100)
	if len(entries) != 0 {
		t.Errorf("expected empty entries for empty file, got %d entries", len(entries))
	}
}

func TestParseSessionLogStream_File_MissingFile(t *testing.T) {
	_, err := os.Open("/nonexistent/path.jsonl")
	if err == nil {
		t.Fatal("expected open error for missing file")
	}
}

func TestParseSessionLogStream_File_MalformedLines(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "bad.jsonl")

	lines := `not json
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"ok.go"}}]}}
also not json
`
	os.WriteFile(logPath, []byte(lines), 0o644)

	entries := parseLogFileUpdates(t, logPath, 100)
	// Only the valid assistant line should produce an entry
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry (skipping malformed), got %d", len(entries))
	}
}

func TestExtractPlanFilePath_Success(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "session.jsonl")

	lines := `{"type":"user","message":{"content":[{"type":"text","text":"plan something"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"I will create a plan."}]}}
{"type":"attachment","attachment":{"type":"plan_mode","planFilePath":"/Users/test/.claude/plans/abc123.md"}}
{"type":"assistant","message":{"content":[{"type":"text","text":"Done."}]}}
`
	if err := os.WriteFile(logPath, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ExtractPlanFilePath(logPath)
	if err != nil {
		t.Fatalf("ExtractPlanFilePath: %v", err)
	}
	if got != "/Users/test/.claude/plans/abc123.md" {
		t.Errorf("got %q, want /Users/test/.claude/plans/abc123.md", got)
	}
}

func TestExtractPlanFilePath_EarlyExit(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "session.jsonl")

	// Two attachments — should return the first one immediately.
	lines := `{"type":"attachment","attachment":{"type":"plan_mode","planFilePath":"/Users/test/.claude/plans/first.md"}}
{"type":"attachment","attachment":{"type":"plan_mode","planFilePath":"/Users/test/.claude/plans/second.md"}}
`
	if err := os.WriteFile(logPath, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ExtractPlanFilePath(logPath)
	if err != nil {
		t.Fatalf("ExtractPlanFilePath: %v", err)
	}
	if got != "/Users/test/.claude/plans/first.md" {
		t.Errorf("got %q, want first.md path", got)
	}
}

func TestExtractPlanFilePath_NoAttachment(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "session.jsonl")

	lines := `{"type":"user","message":{"content":[{"type":"text","text":"hello"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}
`
	if err := os.WriteFile(logPath, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ExtractPlanFilePath(logPath)
	if err == nil {
		t.Fatal("expected error for missing plan_mode attachment")
	}
}

func TestExtractPlanFilePath_MissingFile(t *testing.T) {
	_, err := ExtractPlanFilePath("/nonexistent/path.jsonl")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
