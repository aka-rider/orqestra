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

// NewSessionDir creates and returns a new session directory under .orqestra/sessions/.
// The directory name includes a timestamp and optional slug for identification.
func NewSessionDir(repoPath, slug string) (SessionDir, error) {
	ts := time.Now().Format("2006-01-02-150405")
	name := ts
	if slug != "" {
		name = ts + "-" + slug
	}
	dir := filepath.Join(repoPath, ".orqestra", "sessions", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return SessionDir{}, fmt.Errorf("creating session dir %s: %w", dir, err)
	}
	return SessionDir{Path: dir}, nil
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
