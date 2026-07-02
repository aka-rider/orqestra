package harness

import (
	"os"
	"path/filepath"
	"strings"
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

func TestParseSessionLogStream_File_ToolUseAndText(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "test.jsonl")

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

func TestParseSessionLogStream_File_MalformedLines(t *testing.T) {
	// INV-P4-STREAM: non-JSON lines degrade gracefully (skipped, not a fatal error)
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "bad.jsonl")

	lines := `not json
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"ok.go"}}]}}
also not json
`
	os.WriteFile(logPath, []byte(lines), 0o644)

	entries := parseLogFileUpdates(t, logPath, 100)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry (skipping malformed), got %d", len(entries))
	}
}

// TestCwdToDash_FoldsDotAndUnderscore pins CwdToDash to Claude CLI's actual
// project-directory naming scheme, confirmed empirically (J38) by comparing
// this machine's real ~/.claude/projects/ entries against the "cwd" field
// recorded inside their session JSONL: a session run from "/Users/xiii/.claude"
// is stored under "-Users-xiii--claude" (the "." folded to "-", not kept
// literally), and a session run from a path containing "_" (a Go t.TempDir()
// path such as ".../TestClaudeCLI_InSandbox1202853231/001") is stored with the
// "_" folded to "-" as well. Before this fix, CwdToDash only folded "/",
// leaving "." and "_" untouched — every JSONL-path lookup for a repo/tempdir
// whose path contains "." or "_" would resolve to the wrong project directory.
func TestCwdToDash_FoldsDotAndUnderscore(t *testing.T) {
	// INV-H1-CWDDASH: "." and "_" are folded to "-", matching observed Claude CLI behavior.
	got := CwdToDash("/Users/xiii/.claude")
	want := "-Users-xiii--claude"
	if got != want {
		t.Errorf("CwdToDash(%q) = %q, want %q", "/Users/xiii/.claude", got, want)
	}

	got2 := CwdToDash("/private/var/folders/q4/bq1h62_d2ls9hqp7j501tr2c0000gn/T/TestClaudeCLI_InSandbox1202853231/001")
	want2 := "-private-var-folders-q4-bq1h62-d2ls9hqp7j501tr2c0000gn-T-TestClaudeCLI-InSandbox1202853231-001"
	if got2 != want2 {
		t.Errorf("CwdToDash(%q) = %q, want %q", "/private/var/folders/.../bq1h62_.../TestClaudeCLI_InSandbox.../001", got2, want2)
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

// TestExtractPlanFilePath_LargeLineBeforeAttachment verifies that a JSONL line
// in the 1-2 MB range (below maxJSONLLineBytes, above the old 1 MB ceiling)
// does not abort the scan before the plan_mode attachment line is reached.
// Before the fix, ExtractPlanFilePath used a 1 MB bufio.Scanner buffer while
// ExtractFinalOutput and the stream parser used the shared 2 MB
// maxJSONLLineBytes — a large intervening line (e.g. a big tool_result or
// assistant message) made bufio.ErrTooLong abort the scan, surfacing as
// "no plan_mode attachment found" even though the attachment was present.
func TestExtractPlanFilePath_LargeLineBeforeAttachment(t *testing.T) {
	// INV-P1-PLANSRC: a large-but-within-ceiling line must not break plan-path extraction.
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "session.jsonl")

	// Build one JSONL line whose total length is ~1.5 MB (over the old 1 MB
	// scanner buffer, under the shared 2 MB maxJSONLLineBytes ceiling).
	const bigTextLen = 3 * 512 * 1024 // 1.5 MB
	bigText := strings.Repeat("x", bigTextLen)
	bigLine := `{"type":"assistant","message":{"content":[{"type":"text","text":"` + bigText + `"}]}}`

	var b strings.Builder
	b.WriteString(`{"type":"user","message":{"content":[{"type":"text","text":"plan something"}]}}` + "\n")
	b.WriteString(bigLine + "\n")
	b.WriteString(`{"type":"attachment","attachment":{"type":"plan_mode","planFilePath":"/Users/test/.claude/plans/abc123.md"}}` + "\n")
	if err := os.WriteFile(logPath, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ExtractPlanFilePath(logPath)
	if err != nil {
		t.Fatalf("ExtractPlanFilePath with 1.5MB intervening line: %v", err)
	}
	if got != "/Users/test/.claude/plans/abc123.md" {
		t.Errorf("got %q, want /Users/test/.claude/plans/abc123.md", got)
	}
}

func TestExtractPlanFilePath_NoAttachment(t *testing.T) {
	// INV-P1-PLANSRC: missing plan_mode attachment must return an error
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
