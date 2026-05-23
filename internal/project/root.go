package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	projectDir = ".orqestra"
	gitDir     = ".git"
	sessions   = "sessions"
)

// ErrNoGitRepo is returned when the directory has no .git subdirectory.
var ErrNoGitRepo = errors.New("not a git repository: orqestra must be run from a git project root")

// CheckGitRoot verifies that dir contains a .git subdirectory.
// Returns ErrNoGitRepo if not.
func CheckGitRoot(dir string) error {
	if !isDir(filepath.Join(dir, gitDir)) {
		return fmt.Errorf("%w: %s", ErrNoGitRepo, dir)
	}
	return nil
}

// IsInitialized reports whether dir has an .orqestra/ directory.
func IsInitialized(dir string) bool {
	return isDir(filepath.Join(dir, projectDir))
}

// Init creates .orqestra/sessions/ at root, adds .orqestra/ to .gitignore.
// Fails if .orqestra already exists.
func Init(root string) error {
	projectPath := filepath.Join(root, projectDir)

	if err := os.Mkdir(projectPath, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", projectPath, err)
	}

	sessionsPath := filepath.Join(projectPath, sessions)
	if err := os.Mkdir(sessionsPath, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", sessionsPath, err)
	}

	if err := addToGitignore(root, projectDir); err != nil {
		return fmt.Errorf("update .gitignore: %w", err)
	}

	return nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

func addToGitignore(root, entry string) error {
	gitignore := filepath.Join(root, ".gitignore")
	entryWithSlash := entry + "/"

	data, err := os.ReadFile(gitignore)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Create new .gitignore
			return os.WriteFile(gitignore, []byte(entryWithSlash+"\n"), 0o644)
		}
		return err
	}

	existing := string(data)
	if strings.Contains(existing, entryWithSlash) || strings.Contains(existing, entry+"\n") || strings.Contains(existing, entry+"\r") {
		return nil // already present
	}

	// Ensure trailing newline before appending
	appendText := ""
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		appendText = "\n"
	}
	appendText += entryWithSlash + "\n"

	return os.WriteFile(gitignore, []byte(existing+appendText), 0o644)
}
