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

	var lineCount int
	var typesSeen []string
	for scanner.Scan() {
		lineCount++
		var msg jsonlAttachmentMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		typesSeen = append(typesSeen, msg.Type)
		if msg.Type == "attachment" && msg.Attachment.Type == "plan_mode" && msg.Attachment.PlanFilePath != "" {
			slog.Debug("found plan_mode attachment in JSONL",
				"path", sessionLogPath, "line", lineCount, "plan_file", msg.Attachment.PlanFilePath)
			return msg.Attachment.PlanFilePath, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan session log %q: %w", sessionLogPath, err)
	}
	slog.Debug("no plan_mode attachment in JSONL",
		"path", sessionLogPath, "lines_scanned", lineCount, "types_seen", typesSeen)
	return "", fmt.Errorf("no plan_mode attachment found in %q (%d lines scanned)", sessionLogPath, lineCount)
}
