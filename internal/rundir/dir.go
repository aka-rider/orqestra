// Package rundir owns the one artifact schema for a pipeline run's session
// directory (.orqestra/sessions/<timestamp>[-<slug>]/): the file layout, the
// typed Save/Load accessors for the well-known artifacts, and the StepMeta
// schema persisted per agent. Both the write side (orchestrator steps, via
// ArtifactSink) and the read side (orchestrator.ListRuns/LoadRunDetail) go
// through this package so historical runs and freshly-written runs agree on
// one shape (J11: previously a dead writer — artifacts.go, deleted in WP6 —
// and the live writer disagreed on the schema the reader expected).
package rundir

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/xiii/orqestra/internal/agent"
)

// Dir is the one typed handle onto a run's session directory — the schema's
// read AND write side share this type.
type Dir struct {
	Path string // absolute path to the session directory
}

// Create creates a new run/session directory under root/.orqestra/sessions/
// and returns it as a schema-typed Dir. Directory-creation semantics (naming,
// mkdir strategy) are unchanged from agent.NewSessionDir — that part of the
// schema is out of WP15's scope; Create wraps it rather than duplicating it.
func Create(root, slug string) (Dir, error) {
	sd, err := agent.NewSessionDir(root, slug)
	if err != nil {
		return Dir{}, err
	}
	return Dir{Path: sd.Path}, nil
}

// path returns the absolute path for a named artifact file within the dir.
func (d Dir) path(name string) string {
	return filepath.Join(d.Path, name)
}

// SaveArtifact writes an arbitrary named artifact under the run directory.
// Callers decide whether a failure is fail-closed (propagate) or best-effort
// (log and continue) — SaveArtifact itself always reports the truth.
func (d Dir) SaveArtifact(name string, data []byte) error {
	if d.Path == "" {
		return fmt.Errorf("rundir: directory path not set, cannot write %q", name)
	}
	if err := os.WriteFile(d.path(name), data, 0o644); err != nil {
		return fmt.Errorf("rundir: write %q: %w", d.path(name), err)
	}
	return nil
}

// LoadArtifact reads a named artifact. A missing file is not an error — most
// artifacts are optional (e.g. an older or partial run may lack
// worker_validation.txt) — LoadArtifact returns ("", false, nil) for that
// case. Any other read failure is a genuine error, wrapped and returned so
// callers can distinguish "absent" from "unreadable" (§1.6: optional
// discovery may return empty, but a real failure must not be swallowed).
func (d Dir) LoadArtifact(name string) (content string, present bool, err error) {
	data, readErr := os.ReadFile(d.path(name))
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("rundir: read %q: %w", d.path(name), readErr)
	}
	return string(data), true, nil
}
