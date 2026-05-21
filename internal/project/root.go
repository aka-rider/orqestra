package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotInitialized is returned when no .orqestra or .git marker is found
// while walking upward from the given directory.
var ErrNotInitialized = errors.New("project not initialized")

const (
	projectDir = ".orqestra"
	gitDir     = ".git"
	sessions   = "sessions"
)

// FindRoot walks upward from baseDir looking for a .orqestra directory,
// falling back to the nearest .git directory. Returns the absolute canonical
// path or an error wrapping ErrNotInitialized.
func FindRoot(baseDir string) (string, error) {
	abs, err := filepath.Abs(baseDir)
	if err != nil {
		return "", fmt.Errorf("resolve base dir %s: %w", baseDir, err)
	}

	// First pass: look for .orqestra (explicit project marker)
	if root := walkUp(abs, func(dir string) bool {
		return isDir(filepath.Join(dir, projectDir))
	}); root != "" {
		return canonical(root)
	}

	// Second pass: look for .git (repository marker)
	if root := walkUp(abs, func(dir string) bool {
		return isDir(filepath.Join(dir, gitDir))
	}); root != "" {
		return canonical(root)
	}

	return "", fmt.Errorf("%w: no .orqestra or .git found above %s", ErrNotInitialized, baseDir)
}

// FindGitRoot walks upward from baseDir looking only for a .git directory.
// Returns the absolute canonical path or an error.
func FindGitRoot(baseDir string) (string, error) {
	abs, err := filepath.Abs(baseDir)
	if err != nil {
		return "", fmt.Errorf("resolve base dir %s: %w", baseDir, err)
	}

	if root := walkUp(abs, func(dir string) bool {
		return isDir(filepath.Join(dir, gitDir))
	}); root != "" {
		return canonical(root)
	}

	return "", fmt.Errorf("no .git found above %s", baseDir)
}

// IsGitRoot returns true if dir contains a .git subdirectory.
func IsGitRoot(dir string) bool {
	return isDir(filepath.Join(dir, gitDir))
}

// Init creates .orqestra/sessions/ at root, adds .orqestra/ to .gitignore,
// and creates the sessions subdirectory. Idempotent: no error if .orqestra
// already exists.
func Init(root string) error {
	projectPath := filepath.Join(root, projectDir)
	sessionsPath := filepath.Join(projectPath, sessions)

	if err := os.MkdirAll(sessionsPath, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", sessionsPath, err)
	}

	if err := addToGitignore(root, projectDir); err != nil {
		return fmt.Errorf("update .gitignore: %w", err)
	}

	return nil
}

// walkUp walks from start to filesystem root, calling check at each directory.
// Returns the first directory where check returns true, or "" if none match.
func walkUp(start string, check func(dir string) bool) string {
	dir := start
	for {
		if check(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached filesystem root
		}
		dir = parent
	}
	return ""
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

func canonical(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path, nil // best effort: return path if symlink evaluation fails
	}
	return resolved, nil
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
