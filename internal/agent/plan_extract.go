package agent

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/xiii/orqestra/internal/harness"
)

// ReadPlanFromRun reads the plan content written by Claude CLI's plan mode.
// It locates the plan file via the session JSONL's plan_mode attachment, or
// falls back to scanning ~/.claude/plans/ for recently modified files.
// The returned content is normalized (# Plan header prepended if missing).
func ReadPlanFromRun(result harness.RunResult) (string, error) {
	if result.SessionID == "" {
		return "", fmt.Errorf("no session ID in run result")
	}

	// If the stream captured the plan file path directly, use it.
	if result.PlanFilePath != "" {
		content, err := readSecurePlanFile(result.PlanFilePath)
		if err == nil {
			return normalizePlanHeader(content), nil
		}
		slog.Debug("plan file path from stream invalid, falling back to JSONL scan",
			"path", result.PlanFilePath, "err", err)
	}

	// Resolve session JSONL and extract plan file path from plan_mode attachment.
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get cwd: %w", err)
	}

	jsonlPath, err := harness.ResolveSessionLogPath(cwd, result.SessionID)
	if err != nil {
		return "", fmt.Errorf("resolve session log for %s: %w", result.SessionID, err)
	}

	planFilePath, err := harness.ExtractPlanFilePath(jsonlPath)
	if err != nil {
		// Fallback: scan ~/.claude/plans/ for recently modified .md files.
		slog.Debug("no plan_mode attachment in JSONL, scanning plans directory",
			"session_id", result.SessionID, "err", err)
		fallbackContent, fbErr := scanPlansDirectory()
		if fbErr != nil {
			return "", fmt.Errorf("extract plan for session %s: JSONL scan failed (%w), plans dir scan failed (%w)", result.SessionID, err, fbErr)
		}
		return normalizePlanHeader(fallbackContent), nil
	}

	content, err := readSecurePlanFile(planFilePath)
	if err != nil {
		return "", fmt.Errorf("read plan file for session %s: %w", result.SessionID, err)
	}
	return normalizePlanHeader(content), nil
}

// readSecurePlanFile reads a plan file after verifying it resides under ~/.claude/plans/.
func readSecurePlanFile(planFilePath string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	allowedPrefix := filepath.Join(home, ".claude", "plans") + string(filepath.Separator)
	absPath, err := filepath.Abs(planFilePath)
	if err != nil {
		return "", fmt.Errorf("resolve plan file path: %w", err)
	}
	if !strings.HasPrefix(absPath, allowedPrefix) {
		return "", fmt.Errorf("plan file %q is outside allowed directory %q", absPath, allowedPrefix)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("read plan file %q: %w", absPath, err)
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return "", fmt.Errorf("plan file %q is empty", absPath)
	}
	return content, nil
}

// scanPlansDirectory finds the most recently modified .md file in ~/.claude/plans/.
// Used as a fallback when the JSONL log doesn't contain a plan_mode attachment.
func scanPlansDirectory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	plansDir := filepath.Join(home, ".claude", "plans")
	entries, err := os.ReadDir(plansDir)
	if err != nil {
		return "", fmt.Errorf("read plans directory %q: %w", plansDir, err)
	}

	var newest string
	var newestMod int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, infoErr := e.Info()
		if infoErr != nil {
			continue
		}
		modTime := info.ModTime().Unix()
		if modTime > newestMod {
			newestMod = modTime
			newest = filepath.Join(plansDir, e.Name())
		}
	}
	if newest == "" {
		return "", fmt.Errorf("no .md files found in %q", plansDir)
	}

	slog.Info("plan file resolved via directory scan fallback", "path", newest)
	data, err := os.ReadFile(newest)
	if err != nil {
		return "", fmt.Errorf("read plan file %q: %w", newest, err)
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return "", fmt.Errorf("plan file %q is empty", newest)
	}
	return content, nil
}

// normalizePlanHeader prepends "# Plan\n\n" if content starts with
// "## Goal" or "## Context" but not "# Plan". Claude Code's plan files
// sometimes omit the top-level heading.
func normalizePlanHeader(md string) string {
	trimmed := strings.TrimSpace(md)
	if strings.HasPrefix(trimmed, "# Plan") {
		return trimmed
	}
	if strings.HasPrefix(trimmed, "## Goal") || strings.HasPrefix(trimmed, "## Context") {
		return "# Plan\n\n" + trimmed
	}
	return trimmed
}

// truncateRaw limits a raw string for error messages.
func truncateRaw(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
