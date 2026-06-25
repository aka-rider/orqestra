package tui

import (
	"fmt"
	"os"
	"path/filepath"
)

// planTempFile writes the plan markdown to a fresh temp file the external editor
// edits in place. The caller reads it back on editor exit. We never edit the
// orchestrator's own plan file underneath it — the worker runs only the edited
// markdown that comes back through the edit-confirm gate. The path is returned
// for the round-trip; on any write failure we propagate so the gate fails closed
// (keeps the original plan, surfaces the error) rather than opening an empty file.
func planTempFile(markdown string) (string, error) {
	f, err := os.CreateTemp("", "orqestra-plan-*.md")
	if err != nil {
		return "", fmt.Errorf("create plan temp file: %w", err)
	}
	path := f.Name()
	if _, err := f.WriteString(markdown); err != nil {
		_ = f.Close()                  // fire-and-forget: the write already failed; close error is moot
		_ = os.Remove(path)            // fire-and-forget: best-effort cleanup of the unusable temp file
		return "", fmt.Errorf("write plan to %s: %w", filepath.Base(path), err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path) // fire-and-forget: best-effort cleanup of the unflushed temp file
		return "", fmt.Errorf("flush plan to %s: %w", filepath.Base(path), err)
	}
	return path, nil
}
