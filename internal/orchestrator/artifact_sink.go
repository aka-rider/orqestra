package orchestrator

import (
	"fmt"
	"log/slog"

	"github.com/xiii/orqestra/internal/agent"
)

// ArtifactSink separates integrity writes (fail-closed) from diagnostic writes
// (warn-and-continue). Callers MUST propagate errors from Write.
type ArtifactSink interface {
	// Write persists an integrity artifact. Returns a wrapped error with path
	// context; the caller MUST propagate failure — it never silently skips.
	Write(name string, data []byte) error

	// WriteBestEffort persists a diagnostic artifact. Logs errors and continues;
	// the caller may discard the returned error.
	WriteBestEffort(name string, data []byte)
}

// sessionArtifactSink implements ArtifactSink backed by an agent.SessionDir.
type sessionArtifactSink struct {
	dir agent.SessionDir
}

// NewArtifactSink returns an ArtifactSink that writes to the given session directory.
// If dir.Path is empty, Write returns an error and WriteBestEffort is a no-op.
func NewArtifactSink(dir agent.SessionDir) ArtifactSink {
	return &sessionArtifactSink{dir: dir}
}

func (s *sessionArtifactSink) Write(name string, data []byte) error {
	if s.dir.Path == "" {
		return fmt.Errorf("artifact_sink: session directory not set, cannot write %q", name)
	}
	if err := s.dir.WriteArtifact(name, data); err != nil {
		return fmt.Errorf("artifact_sink: write %q: %w", s.dir.ArtifactPath(name), err)
	}
	return nil
}

func (s *sessionArtifactSink) WriteBestEffort(name string, data []byte) {
	if s.dir.Path == "" {
		return
	}
	if err := s.dir.WriteArtifact(name, data); err != nil {
		slog.Error("artifact_sink: best-effort write failed", "path", s.dir.ArtifactPath(name), "err", err)
	}
}

// noopArtifactSink discards all writes. Useful in tests.
type noopArtifactSink struct{}

func NoopArtifactSink() ArtifactSink { return noopArtifactSink{} }

func (noopArtifactSink) Write(_ string, _ []byte) error  { return nil }
func (noopArtifactSink) WriteBestEffort(_ string, _ []byte) {}
