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

// ResolvePlanFilePath returns the on-disk path to a session's plan file,
// using the SAME resolution order as ReadPlanFile (the stream-reported path,
// then the JSONL plan_mode attachment) — but it only resolves the PATH; it
// never reads or security-validates the file's content. Used by report-
// freshness snapshots (WP11/J35) that need to stat a plan file's mtime/size
// before AND after an invocation without trusting anything read from it —
// the actual content read for a harvested report still goes exclusively
// through ReadPlanFile → readSecurePlanFile's full symlink-containment
// check.
func ResolvePlanFilePath(sessionID, planFilePath, repoCWD string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("no session ID")
	}
	if planFilePath != "" {
		return planFilePath, nil
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
	return jsonlPlanPath, nil
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
