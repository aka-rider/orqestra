package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SessionDir manages the host-side session directory where pipeline artifacts accumulate.
type SessionDir struct {
	Path string // absolute path to the session directory
}

// SubDir returns the path of a subdirectory under the session directory.
func (s SessionDir) SubDir(name string) string { return filepath.Join(s.Path, name) }

// ResearchDir returns the path of the research subdirectory.
func (s SessionDir) ResearchDir() string { return s.SubDir("research") }

// DeliberationDir returns the path of the deliberation subdirectory.
func (s SessionDir) DeliberationDir() string { return s.SubDir("deliberation") }

// ExecutionDir returns the path of the execution subdirectory.
func (s SessionDir) ExecutionDir() string { return s.SubDir("execution") }

// ValidationDir returns the path of the validation subdirectory.
func (s SessionDir) ValidationDir() string { return s.SubDir("validation") }

// NewSessionDir creates and returns a new session directory under .orqestra/sessions/.
// The directory name includes a timestamp and optional slug for identification.
func NewSessionDir(repoPath, slug string) (SessionDir, error) {
	ts := time.Now().Format("2006-01-02-150405")
	name := ts
	if slug != "" {
		name = ts + "-" + slug
	}
	dir := filepath.Join(repoPath, ".orqestra", "sessions", name)
	if err := mkdirAll(dir, 0o755); err != nil {
		return SessionDir{}, fmt.Errorf("creating session dir %s: %w", dir, err)
	}
	return SessionDir{Path: dir}, nil
}

// mkdir creates a single directory (not nested). Returns ErrExist-wrapped error
// if the directory already exists, or a wrapped permission error otherwise.
func mkdir(path string, perm os.FileMode) error {
	err := os.Mkdir(path, perm)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("directory already exists: %w", err)
		}
		return fmt.Errorf("mkdir %s: %w", path, err)
	}
	return nil
}

// mkdirAll creates a directory and all ancestors. If the leaf already exists
// as a directory, it returns nil (no error). If the leaf exists as a file,
// or if any other error occurs, it returns a wrapped error.
func mkdirAll(dir string, perm os.FileMode) error {
	// Fast path: if it already exists as a directory, we're done.
	info, err := os.Stat(dir)
	if err == nil && info.IsDir() {
		return nil
	}
	// Otherwise use standard MkdirAll for the hierarchy, then verify leaf.
	if err := os.MkdirAll(dir, perm); err != nil {
		return fmt.Errorf("mkdirAll %s: %w", dir, err)
	}
	// Verify the leaf is a directory (MkdirAll can succeed even if a file
	// with the same name existed but was replaced by a symlink etc.).
	info, err = os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("path exists but is not a directory: %s", dir)
	}
	return nil
}

// ArtifactPath returns the absolute path for a named artifact within the session.
func (s SessionDir) ArtifactPath(name string) string {
	return filepath.Join(s.Path, name)
}

// WriteArtifact writes an artifact to the session directory.
func (s SessionDir) WriteArtifact(name string, data []byte) error {
	path := s.ArtifactPath(name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing artifact %s: %w", name, err)
	}
	return nil
}

// ReadArtifact reads an artifact from the session directory.
func (s SessionDir) ReadArtifact(name string) ([]byte, error) {
	path := s.ArtifactPath(name)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading artifact %s: %w", name, err)
	}
	return data, nil
}

// SessionLogResolver resolves the on-disk path of a Claude CLI session JSONL.
type SessionLogResolver func(repoPath, sessionID string) (string, error)

// CopySessionLog copies the Claude CLI session JSONL for sessionID into this session
// directory as destName (e.g., "researcher_session.jsonl").
// repoPath is the repository root used to locate the source JSONL.
// Returns ("", nil) if sessionID is empty or if the session dir is unset.
// Returns ("", err) on IO failure — callers should slog.Warn and continue.
func CopySessionLog(s SessionDir, repoPath, sessionID, destName string, resolve SessionLogResolver) (string, error) {
	if sessionID == "" || s.Path == "" {
		return "", nil
	}
	src, err := resolve(repoPath, sessionID)
	if err != nil {
		return "", fmt.Errorf("copy session log: resolve %s: %w", sessionID, err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("copy session log: read %s: %w", src, err)
	}
	if err := s.WriteArtifact(destName, data); err != nil {
		return "", fmt.Errorf("copy session log: write %s: %w", destName, err)
	}
	return s.ArtifactPath(destName), nil
}
