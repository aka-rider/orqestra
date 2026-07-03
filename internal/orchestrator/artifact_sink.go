package orchestrator

import (
	"log/slog"

	"github.com/xiii/orqestra/internal/rundir"
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

// sessionArtifactSink implements ArtifactSink backed by a rundir.Dir (WP15:
// rundir owns the artifact schema; ArtifactSink is a thin two-tier adapter
// over it — Write/WriteBestEffort decide fail-closed vs best-effort, rundir
// decides the actual file layout).
type sessionArtifactSink struct {
	dir rundir.Dir
	log *slog.Logger
}

// NewArtifactSink returns an ArtifactSink that writes to the given session directory.
// If dir.Path is empty, Write returns an error and WriteBestEffort is a no-op.
// log is the per-run injected logger (StepContext.Log) — never slog.Default().
func NewArtifactSink(dir rundir.Dir, log *slog.Logger) ArtifactSink {
	return &sessionArtifactSink{dir: dir, log: log}
}

func (s *sessionArtifactSink) Write(name string, data []byte) error {
	return s.dir.SaveArtifact(name, data)
}

func (s *sessionArtifactSink) WriteBestEffort(name string, data []byte) {
	if s.dir.Path == "" {
		return
	}
	if err := s.dir.SaveArtifact(name, data); err != nil {
		s.log.Error("artifact_sink: best-effort write failed", "name", name, "err", err)
	}
}

// noopArtifactSink discards all writes. Useful in tests.
type noopArtifactSink struct{}

func NoopArtifactSink() ArtifactSink { return noopArtifactSink{} }

func (noopArtifactSink) Write(_ string, _ []byte) error     { return nil }
func (noopArtifactSink) WriteBestEffort(_ string, _ []byte) {}
