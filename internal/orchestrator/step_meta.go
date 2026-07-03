package orchestrator

import (
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/rundir"
)

// StepMeta is an alias for rundir.StepMeta. rundir owns the artifact schema
// (WP15/J11): the field set, JSON tags, and file-naming convention all live
// there so the write side (steps, via ArtifactSink) and the read side
// (ListRuns/LoadRunDetail) can never drift apart again. orchestrator keeps
// the alias so every existing "StepMeta{...}" call site in this package
// (step_deliberate.go, step_execute.go, step_validate.go, run_history.go)
// compiles unchanged — orchestrator decides WHEN a step writes one; rundir
// decides WHAT it looks like and how it's persisted.
type StepMeta = rundir.StepMeta

// resolveSessionLogPath best-effort resolves the on-disk path of a Claude CLI
// session JSONL, for population into StepMeta.ClaudeSessionLogPath (J43: the
// run-detail log viewer — internal/tui/screen_run_detail_log.go — reads this
// field, but until now no writeMeta ever set it). An unresolvable path
// (missing session ID, no RepoPath, JSONL not yet on disk) stays "" — this is
// diagnostic metadata, not an integrity boundary, so resolution failure must
// never fail the step it's attached to.
func resolveSessionLogPath(sc StepContext, sessionID string) string {
	if sessionID == "" || sc.RepoPath == "" {
		return ""
	}
	path, err := harness.ResolveSessionLogPath(sc.RepoPath, sessionID)
	if err != nil {
		return "" // fire-and-forget: best-effort diagnostic metadata (J43)
	}
	return path
}
