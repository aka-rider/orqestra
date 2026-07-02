package agent

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/xiii/orqestra/internal/harness"
)

// ReadPlan reads the plan content written by Claude CLI's plan mode.
// It locates the plan file via planFilePath (from the run result stream), the
// session JSONL's plan_mode attachment, or falls back to scanning ~/.claude/plans/.
// repoCWD is the repository root used to resolve the session JSONL path.
// withFallback enables a last-resort tier-4: when the plan file was not written,
// the final assistant message is read directly from the session JSONL (text blocks
// first, thinking blocks second). Pass false for continuation/revision sessions
// where a non-write is a valid "no change". fromFallback=true signals that the
// model disobeyed the plan-writing instruction — callers should log this as a canary.
func ReadPlan(sessionID, planFilePath, repoCWD string, withFallback bool) (content string, fromFallback bool, err error) {
	if sessionID == "" {
		return "", false, fmt.Errorf("no session ID")
	}

	// If the stream captured the plan file path directly, use it.
	if planFilePath != "" {
		c, readErr := readSecurePlanFile(planFilePath)
		if readErr == nil {
			return strings.TrimSpace(c), false, nil
		}
		slog.Debug("plan file path from stream invalid, falling back to JSONL scan",
			"path", planFilePath, "err", readErr)
	}

	// Resolve session JSONL and extract plan file path from plan_mode attachment.
	cwd := repoCWD
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return "", false, fmt.Errorf("get cwd: %w", err)
		}
	}

	jsonlPath, err := harness.ResolveSessionLogPath(cwd, sessionID)
	if err != nil {
		return "", false, fmt.Errorf("resolve session log for %s: %w", sessionID, err)
	}

	jsonlPlanPath, err := harness.ExtractPlanFilePath(jsonlPath)
	if err != nil {
		// Fallback: scan ~/.claude/plans/ for recently modified .md files.
		slog.Debug("no plan_mode attachment in JSONL, scanning plans directory",
			"session_id", sessionID, "err", err)
		fallbackContent, fbErr := scanPlansDirectory()
		if fbErr != nil {
			return "", false, fmt.Errorf("extract plan for session %s: JSONL scan failed (%w), plans dir scan failed (%w)", sessionID, err, fbErr)
		}
		return strings.TrimSpace(fallbackContent), false, nil
	}

	c, err := readSecurePlanFile(jsonlPlanPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Tier 4: model did not write the plan file — read its final message from the JSONL.
			// Covers both text-only and thinking-only responses. This is a canary.
			if withFallback {
				if extracted, fbErr := harness.ExtractFinalOutput(jsonlPath); fbErr == nil {
					slog.Warn("plan resolved from session JSONL output — model did not write to plan file",
						"session_id", sessionID, "expected_path", jsonlPlanPath)
					return extracted, true, nil
				}
			}
			return "", false, fmt.Errorf("model session %s completed but did not write a plan file (%s)",
				sessionID, jsonlPlanPath)
		}
		return "", false, fmt.Errorf("read plan file for session %s: %w", sessionID, err)
	}
	return strings.TrimSpace(c), false, nil
}

// ReadPlanFile reads the plan file for the given session using only the definitive
// per-session path: the stream-captured path first, then the JSONL plan_mode attachment.
// It never falls back to scanning ~/.claude/plans/ and never reads the final message —
// either the plan file exists for this session or the function returns an error.
// Use this at orchestrator integrity boundaries where a stale plan from a different
// session must never be silently accepted.
func ReadPlanFile(sessionID, planFilePath, repoCWD string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("no session ID")
	}

	if planFilePath != "" {
		c, readErr := readSecurePlanFile(planFilePath)
		if readErr == nil {
			return strings.TrimSpace(c), nil
		}
		slog.Debug("plan file path from stream invalid, trying JSONL attachment",
			"path", planFilePath, "err", readErr)
	}

	cwd := repoCWD
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get cwd: %w", err)
		}
	}

	jsonlPath, err := harness.ResolveSessionLogPath(cwd, sessionID)
	if err != nil {
		return "", fmt.Errorf("resolve session log for %s: %w", sessionID, err)
	}

	jsonlPlanPath, err := harness.ExtractPlanFilePath(jsonlPath)
	if err != nil {
		return "", fmt.Errorf("no plan file for session %s: JSONL scan failed (%w)", sessionID, err)
	}

	c, err := readSecurePlanFile(jsonlPlanPath)
	if err != nil {
		return "", fmt.Errorf("read plan file for session %s: %w", sessionID, err)
	}
	return strings.TrimSpace(c), nil
}

// readSecurePlanFile reads a plan file after verifying it resides under
// ~/.claude/plans/. The containment check is symlink-resolved, not merely
// lexical: filepath.Abs + strings.HasPrefix alone would accept a symlink
// planted inside ~/.claude/plans/ whose target points outside it (or a
// directory-name prefix collision, e.g. ~/.claude/plans-evil). Both the
// allowed root and the candidate path are resolved with filepath.EvalSymlinks
// before the boundary check, and the prefix check is path-segment-aware
// (trailing separator, or an exact match) so "plans-evil" cannot pass a
// "plans" prefix test.
func readSecurePlanFile(planFilePath string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	allowedRoot := filepath.Join(home, ".claude", "plans")
	resolvedRoot, err := filepath.EvalSymlinks(allowedRoot)
	if err != nil {
		return "", fmt.Errorf("resolve allowed plans directory %q: %w", allowedRoot, err)
	}
	allowedPrefix := resolvedRoot + string(filepath.Separator)

	absPath, err := filepath.Abs(planFilePath)
	if err != nil {
		return "", fmt.Errorf("resolve plan file path: %w", err)
	}

	// Resolve symlinks on the full candidate path so a plan file that is
	// itself a symlink (or sits under a symlinked directory) cannot escape
	// the allowed root. The plan file may legitimately not exist yet (a
	// stream-reported path for a session that never wrote its plan) — in
	// that case fall back to resolving only the containing directory, which
	// still catches a directory-symlink escape, and defer the "does it
	// exist" question to the os.ReadFile call below (same failure shape as
	// before this fix).
	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("resolve plan file path %q: %w", absPath, err)
		}
		resolvedDir, dirErr := filepath.EvalSymlinks(filepath.Dir(absPath))
		if dirErr != nil {
			return "", fmt.Errorf("read plan file %q: %w", absPath, err)
		}
		resolvedPath = filepath.Join(resolvedDir, filepath.Base(absPath))
	}

	if resolvedPath != resolvedRoot && !strings.HasPrefix(resolvedPath, allowedPrefix) {
		return "", fmt.Errorf("plan file %q is outside allowed directory %q", resolvedPath, resolvedRoot)
	}

	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return "", fmt.Errorf("read plan file %q: %w", resolvedPath, err)
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return "", fmt.Errorf("plan file %q is empty", resolvedPath)
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

