package harness

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// CwdToDash converts an absolute path to Claude CLI's project directory naming format.
// /Users/xiii/Developer/orqestra → -Users-xiii-Developer-orqestra
func CwdToDash(absPath string) string {
	return "-" + strings.ReplaceAll(strings.TrimPrefix(absPath, "/"), "/", "-")
}

// ResolveSessionLogPath returns the absolute path to a Claude CLI session JSONL file.
// repoPath is the absolute path to the repository root (used for the projects dir lookup).
func ResolveSessionLogPath(repoPath, sessionID string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("empty session ID")
	}

	resolved, err := filepath.EvalSymlinks(repoPath)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks for %q: %w", repoPath, err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}

	path := filepath.Join(home, ".claude", "projects", CwdToDash(resolved), sessionID+".jsonl")

	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("session log %q: %w", path, err)
	}
	return path, nil
}

// jsonlAttachmentMessage represents a JSONL line with a plan_mode attachment.
type jsonlAttachmentMessage struct {
	Type       string `json:"type"`
	Attachment struct {
		Type         string `json:"type"`
		PlanFilePath string `json:"planFilePath"`
	} `json:"attachment"`
}

// ExtractPlanFilePath scans a session JSONL file for a plan_mode attachment
// and returns the plan file path. Returns an error if no attachment is found.
func ExtractPlanFilePath(sessionLogPath string) (string, error) {
	f, err := os.Open(sessionLogPath)
	if err != nil {
		return "", fmt.Errorf("open session log %q: %w", sessionLogPath, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		var msg jsonlAttachmentMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		if msg.Type == "attachment" && msg.Attachment.Type == "plan_mode" && msg.Attachment.PlanFilePath != "" {
			return msg.Attachment.PlanFilePath, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan session log %q: %w", sessionLogPath, err)
	}
	return "", fmt.Errorf("no plan_mode attachment found in %q", sessionLogPath)
}

// LogEntryKind classifies a parsed JSONL log entry for display.
type LogEntryKind int

const (
	LogEntryToolUse LogEntryKind = iota
	LogEntryText
)

// LogEntry is a single parsed entry from a Claude session JSONL file.
type LogEntry struct {
	Kind     LogEntryKind
	ToolName string // for LogEntryToolUse
	Detail   string // tool detail or text preview
}

// ParseSessionLog reads a Claude session JSONL file and extracts displayable entries.
// Returns at most maxLines entries (newest at end). Returns nil, nil for missing/empty files.
func ParseSessionLog(path string, maxLines int) ([]LogEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open session log: %w", err)
	}
	defer f.Close()

	var entries []LogEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer for large JSONL lines

	for scanner.Scan() {
		line := scanner.Bytes()
		parsed := parseJSONLLine(line)
		entries = append(entries, parsed...)
	}
	if err := scanner.Err(); err != nil {
		slog.Debug("session log scan error", "path", path, "err", err)
	}

	if maxLines > 0 && len(entries) > maxLines {
		entries = entries[len(entries)-maxLines:]
	}
	return entries, nil
}

// jsonlMessage is the top-level structure of a Claude session JSONL line.
type jsonlMessage struct {
	Type    string `json:"type"`
	Message struct {
		Content []jsonlContentBlock `json:"content"`
	} `json:"message"`
}

// jsonlContentBlock represents a content block within an assistant message.
type jsonlContentBlock struct {
	Type  string          `json:"type"`
	Name  string          `json:"name,omitempty"` // for tool_use
	Input json.RawMessage `json:"input,omitempty"`
	Text  string          `json:"text,omitempty"` // for text blocks
}

// parseJSONLLine extracts LogEntry values from a single JSONL line.
// Only assistant messages with tool_use or text blocks are extracted.
func parseJSONLLine(line []byte) []LogEntry {
	var msg jsonlMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return nil
	}

	if msg.Type != "assistant" {
		return nil
	}

	var entries []LogEntry
	for _, block := range msg.Message.Content {
		switch block.Type {
		case "tool_use":
			detail := ToolDetail(block.Name, block.Input)
			entries = append(entries, LogEntry{
				Kind:     LogEntryToolUse,
				ToolName: block.Name,
				Detail:   detail,
			})
		case "text":
			text := strings.TrimSpace(block.Text)
			if text == "" {
				continue
			}
			if len(text) > 120 {
				text = text[:120] + "…"
			}
			entries = append(entries, LogEntry{
				Kind:   LogEntryText,
				Detail: text,
			})
		}
	}
	return entries
}
