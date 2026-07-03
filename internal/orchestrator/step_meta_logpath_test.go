package orchestrator

// INV-J43: internal/tui/screen_run_detail_log.go reads StepMeta.ClaudeSessionLogPath
// as its PRIMARY path to a step's Claude CLI session JSONL, but before WP15 no
// writeMeta ever populated it — the viewer's primary path could never work.
// These tests drive each step's meta writer with a fake-but-real session JSONL
// on disk and assert the persisted *_meta.json carries a non-empty
// claude_session_log_path; the final test proves the read path
// (orchestrator.LoadRunDetail, which feeds internal/tui/screen_run_detail_log.go)
// surfaces it end-to-end.

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/rundir"
)

// setupFakeSessionLog points HOME at a temp dir and writes a minimal session
// JSONL at the path harness.ResolveSessionLogPath(repoPath, sessionID) expects,
// so writeMeta's best-effort resolution succeeds deterministically in tests
// (mirrors internal/agent/plan_extract_test.go's setupPlanFile pattern).
func setupFakeSessionLog(t *testing.T, repoPath, sessionID string) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	resolved, err := filepath.EvalSymlinks(repoPath)
	if err != nil {
		t.Fatal(err)
	}
	projDir := filepath.Join(tmp, ".claude", "projects", harness.CwdToDash(resolved))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(projDir, sessionID+".jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"type":"result"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return jsonlPath
}

func TestDeliberateStep_WriteArchMeta_PopulatesClaudeSessionLogPath(t *testing.T) {
	repoPath := t.TempDir()
	const sessionID = "arch-sid-j43"
	wantPath := setupFakeSessionLog(t, repoPath, sessionID)

	artifacts := newRecordingArtifactSink()
	sc := StepContext{
		Artifacts: artifacts,
		Log:       slog.Default(),
		RepoPath:  repoPath,
	}
	step := &DeliberateStep{ArchSpec: harness.ProcessSpec{AgentID: "architect"}}
	step.writeArchMeta(sc, sessionID, time.Now(), "done", nil, harness.TokenUsage{Input: 1, Output: 2})

	data, ok := artifacts.writes["architect_meta.json"]
	if !ok {
		t.Fatal("expected architect_meta.json to be written")
	}
	var meta StepMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal architect_meta.json: %v", err)
	}
	if meta.ClaudeSessionLogPath == "" {
		t.Fatal("J43: architect_meta.json's claude_session_log_path is empty — the log viewer's primary read path can never work")
	}
	if meta.ClaudeSessionLogPath != wantPath {
		t.Errorf("claude_session_log_path = %q, want %q", meta.ClaudeSessionLogPath, wantPath)
	}
}

func TestExecuteStep_WriteMeta_PopulatesClaudeSessionLogPath(t *testing.T) {
	repoPath := t.TempDir()
	const sessionID = "worker-sid-j43"
	wantPath := setupFakeSessionLog(t, repoPath, sessionID)

	artifacts := newRecordingArtifactSink()
	sc := StepContext{
		Artifacts: artifacts,
		Log:       slog.Default(),
		RepoPath:  repoPath,
	}
	step := &ExecuteStep{RepoPath: repoPath}
	step.writeMeta(sc, sessionID, time.Now(), "done", nil, harness.TokenUsage{Input: 1, Output: 2})

	data, ok := artifacts.writes["worker_meta.json"]
	if !ok {
		t.Fatal("expected worker_meta.json to be written")
	}
	var meta StepMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal worker_meta.json: %v", err)
	}
	if meta.ClaudeSessionLogPath == "" {
		t.Fatal("J43: worker_meta.json's claude_session_log_path is empty — the log viewer's primary read path can never work")
	}
	if meta.ClaudeSessionLogPath != wantPath {
		t.Errorf("claude_session_log_path = %q, want %q", meta.ClaudeSessionLogPath, wantPath)
	}
}

// TestValidateStep_Run_PopulatesClaudeSessionLogPath drives ValidateStep.Run
// fully (not just its writeMeta helper) through a fake Executor, proving the
// fix is reachable via the pipeline's real call path, not only a unit-level
// call to the private writer.
func TestValidateStep_Run_PopulatesClaudeSessionLogPath(t *testing.T) {
	repoPath := t.TempDir()
	const sessionID = "validator-sid-j43"
	wantPath := setupFakeSessionLog(t, repoPath, sessionID)

	exec := &sequencedExecutor{
		results: []harness.RunResult{{SessionID: sessionID, Output: "VALIDATION: PASS\nall good"}},
		errs:    []error{nil},
	}
	artifacts := newRecordingArtifactSink()
	sc := StepContext{
		Exec:      exec,
		Obs:       NewObsStore(),
		Artifacts: artifacts,
		Log:       slog.Default(),
		RepoPath:  repoPath,
	}
	step := &ValidateStep{}
	if _, err := step.Run(context.Background(), ValidateInput{WorkerSessionID: "prior-sid"}, sc); err != nil {
		t.Fatalf("ValidateStep.Run: %v", err)
	}

	data, ok := artifacts.writes["validator_meta.json"]
	if !ok {
		t.Fatal("expected validator_meta.json to be written")
	}
	var meta StepMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal validator_meta.json: %v", err)
	}
	if meta.ClaudeSessionLogPath == "" {
		t.Fatal("J43: validator_meta.json's claude_session_log_path is empty — the log viewer's primary read path can never work")
	}
	if meta.ClaudeSessionLogPath != wantPath {
		t.Errorf("claude_session_log_path = %q, want %q", meta.ClaudeSessionLogPath, wantPath)
	}
}

// TestLoadRunDetail_SurfacesClaudeSessionLogPath proves the read path the TUI
// depends on (internal/tui/screen_run_detail_log.go reads
// RunDetail.Steps[i].ClaudeSessionLogPath) actually receives the value a real
// step write persists — a true end-to-end proof of the J43 fix, from writer
// to the exact reader the bug report named.
func TestLoadRunDetail_SurfacesClaudeSessionLogPath(t *testing.T) {
	repoPath := t.TempDir()
	const sessionID = "worker-sid-j43-e2e"
	wantPath := setupFakeSessionLog(t, repoPath, sessionID)

	runDir, err := rundir.Create(repoPath, "run")
	if err != nil {
		t.Fatalf("rundir.Create: %v", err)
	}

	artifacts := NewArtifactSink(runDir, slog.Default())
	sc := StepContext{
		Artifacts: artifacts,
		Log:       slog.Default(),
		RepoPath:  repoPath,
	}
	step := &ExecuteStep{RepoPath: repoPath}
	step.writeMeta(sc, sessionID, time.Now(), "done", nil, harness.TokenUsage{Input: 3, Output: 4})

	detail, err := LoadRunDetail(runDir.Path)
	if err != nil {
		t.Fatalf("LoadRunDetail: %v", err)
	}
	if len(detail.Steps) != 1 {
		t.Fatalf("LoadRunDetail returned %d steps, want 1", len(detail.Steps))
	}
	if detail.Steps[0].ClaudeSessionLogPath == "" {
		t.Fatal("J43: LoadRunDetail's StepMeta.ClaudeSessionLogPath is empty — the TUI log viewer would show \"no agent log available\"")
	}
	if detail.Steps[0].ClaudeSessionLogPath != wantPath {
		t.Errorf("LoadRunDetail ClaudeSessionLogPath = %q, want %q", detail.Steps[0].ClaudeSessionLogPath, wantPath)
	}
}
