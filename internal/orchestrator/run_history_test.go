package orchestrator

// WP15/J11 round-trip proof: the schema rundir writes and the schema
// ListRuns/LoadRunDetail read must be the SAME schema. Before WP15, a dead
// writer (artifacts.go, deleted in WP6) and the live writer disagreed with
// what run_history.go's readStringArtifact/glob scraping expected; these
// tests prove the current single schema round-trips byte-identically, and
// that a historical run written in today's flat file naming (no rundir call
// at all — just files on disk, as a pre-rundir run would have left them)
// still loads correctly.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/rundir"
)

func TestListRuns_LoadRunDetail_RoundTrip(t *testing.T) {
	repoRoot := t.TempDir()

	dir, err := rundir.Create(repoRoot, "roundtrip")
	if err != nil {
		t.Fatalf("rundir.Create: %v", err)
	}

	const (
		prompt     = "build the roundtrip feature"
		plan       = "# Plan\n\nBuild the roundtrip feature."
		workOutput = "worker finished: created roundtrip.go"
		validation = "VALIDATION: PASS\nAll checks green."
	)
	if err := dir.SavePrompt(prompt); err != nil {
		t.Fatalf("SavePrompt: %v", err)
	}
	if err := dir.SaveFinalPlan(plan); err != nil {
		t.Fatalf("SaveFinalPlan: %v", err)
	}
	if err := dir.SaveWorkerOutput(workOutput); err != nil {
		t.Fatalf("SaveWorkerOutput: %v", err)
	}
	if err := dir.SaveValidation(validation); err != nil {
		t.Fatalf("SaveValidation: %v", err)
	}

	base := time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC)
	archMeta := StepMeta{
		AgentID: "architect", StartTime: base, EndTime: base.Add(2 * time.Minute),
		Status: "done", InputTokens: 100, OutputTokens: 50,
	}
	workerMeta := StepMeta{
		AgentID: "worker", StartTime: base.Add(3 * time.Minute), EndTime: base.Add(10 * time.Minute),
		Status: "done", InputTokens: 500, OutputTokens: 300, ClaudeSessionLogPath: "/fake/worker.jsonl",
	}
	if err := dir.SaveStepMeta("architect", archMeta); err != nil {
		t.Fatalf("SaveStepMeta(architect): %v", err)
	}
	if err := dir.SaveStepMeta("worker", workerMeta); err != nil {
		t.Fatalf("SaveStepMeta(worker): %v", err)
	}

	// --- ListRuns ---
	runs, err := ListRuns(repoRoot)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("ListRuns returned %d runs, want 1", len(runs))
	}
	summary := runs[0]
	if summary.Prompt != prompt {
		t.Errorf("ListRuns summary.Prompt = %q, want %q", summary.Prompt, prompt)
	}
	if summary.Status != "done" {
		t.Errorf("ListRuns summary.Status = %q, want %q (from the latest-ending step)", summary.Status, "done")
	}
	wantDuration := workerMeta.EndTime.Sub(archMeta.StartTime)
	if summary.Duration != wantDuration {
		t.Errorf("ListRuns summary.Duration = %v, want %v", summary.Duration, wantDuration)
	}
	wantTokens := archMeta.InputTokens + archMeta.OutputTokens + workerMeta.InputTokens + workerMeta.OutputTokens
	if summary.TotalTokens != wantTokens {
		t.Errorf("ListRuns summary.TotalTokens = %d, want %d", summary.TotalTokens, wantTokens)
	}
	if summary.Path != dir.Path {
		t.Errorf("ListRuns summary.Path = %q, want %q", summary.Path, dir.Path)
	}

	// --- LoadRunDetail ---
	detail, err := LoadRunDetail(dir.Path)
	if err != nil {
		t.Fatalf("LoadRunDetail: %v", err)
	}
	if detail.Prompt != prompt {
		t.Errorf("LoadRunDetail Prompt = %q, want %q", detail.Prompt, prompt)
	}
	if detail.PlanMarkdown != plan {
		t.Errorf("LoadRunDetail PlanMarkdown = %q, want %q", detail.PlanMarkdown, plan)
	}
	if detail.WorkerOutput != workOutput {
		t.Errorf("LoadRunDetail WorkerOutput = %q, want %q", detail.WorkerOutput, workOutput)
	}
	if detail.Validation != validation {
		t.Errorf("LoadRunDetail Validation = %q, want %q", detail.Validation, validation)
	}
	if detail.Status != "done" {
		t.Errorf("LoadRunDetail Status = %q, want %q", detail.Status, "done")
	}
	if detail.Duration != wantDuration {
		t.Errorf("LoadRunDetail Duration = %v, want %v", detail.Duration, wantDuration)
	}
	if detail.TotalTokens != wantTokens {
		t.Errorf("LoadRunDetail TotalTokens = %d, want %d", detail.TotalTokens, wantTokens)
	}
	if len(detail.Steps) != 2 {
		t.Fatalf("LoadRunDetail Steps has %d entries, want 2", len(detail.Steps))
	}
	// Deterministic ordering: architect (earlier StartTime) before worker.
	if detail.Steps[0].AgentID != "architect" || detail.Steps[1].AgentID != "worker" {
		t.Errorf("LoadRunDetail Steps order = [%s, %s], want [architect, worker]",
			detail.Steps[0].AgentID, detail.Steps[1].AgentID)
	}
	// Verdict-bearing / provenance field must round-trip byte-identically.
	if detail.Steps[1].ClaudeSessionLogPath != "/fake/worker.jsonl" {
		t.Errorf("worker step ClaudeSessionLogPath = %q, want /fake/worker.jsonl", detail.Steps[1].ClaudeSessionLogPath)
	}
}

// TestListRuns_LoadRunDetail_LegacyLayoutStillLoads hand-writes a session
// directory using ONLY today's flat file names (as a run written before
// rundir existed would have left on disk) — no rundir.Create, no typed Save
// calls — and proves ListRuns/LoadRunDetail still load it. This is the
// concrete "old runs keep loading" proof WP15 requires.
func TestListRuns_LoadRunDetail_LegacyLayoutStillLoads(t *testing.T) {
	repoRoot := t.TempDir()
	sessionsDir := filepath.Join(repoRoot, ".orqestra", "sessions")
	runName := "2026-01-15-120000-legacy-run"
	runPath := filepath.Join(sessionsDir, runName)
	if err := os.MkdirAll(runPath, 0o755); err != nil {
		t.Fatal(err)
	}

	writeFile := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(runPath, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("prompt.md", "legacy prompt text")
	writeFile("final_plan.md", "# Legacy Plan\n\nDo the legacy thing.")
	writeFile("worker_output.txt", "legacy worker output")
	writeFile("worker_validation.txt", "VALIDATION: PASS")
	writeFile("architect_meta.json", `{
  "agent_id": "architect",
  "start_time": "2026-01-15T12:00:00Z",
  "end_time": "2026-01-15T12:01:00Z",
  "status": "done",
  "input_tokens": 10,
  "output_tokens": 20
}`)
	writeFile("worker_meta.json", `{
  "agent_id": "worker",
  "start_time": "2026-01-15T12:02:00Z",
  "end_time": "2026-01-15T12:05:00Z",
  "status": "done",
  "input_tokens": 30,
  "output_tokens": 40
}`)

	runs, err := ListRuns(repoRoot)
	if err != nil {
		t.Fatalf("ListRuns on legacy layout: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("ListRuns on legacy layout returned %d runs, want 1", len(runs))
	}
	if runs[0].Prompt != "legacy prompt text" {
		t.Errorf("legacy ListRuns Prompt = %q", runs[0].Prompt)
	}
	if runs[0].Slug != "legacy-run" {
		t.Errorf("legacy ListRuns Slug = %q, want %q", runs[0].Slug, "legacy-run")
	}
	if runs[0].Status != "done" {
		t.Errorf("legacy ListRuns Status = %q, want done", runs[0].Status)
	}
	if runs[0].TotalTokens != 100 {
		t.Errorf("legacy ListRuns TotalTokens = %d, want 100", runs[0].TotalTokens)
	}

	detail, err := LoadRunDetail(runPath)
	if err != nil {
		t.Fatalf("LoadRunDetail on legacy layout: %v", err)
	}
	if detail.PlanMarkdown != "# Legacy Plan\n\nDo the legacy thing." {
		t.Errorf("legacy LoadRunDetail PlanMarkdown = %q", detail.PlanMarkdown)
	}
	if detail.WorkerOutput != "legacy worker output" {
		t.Errorf("legacy LoadRunDetail WorkerOutput = %q", detail.WorkerOutput)
	}
	if detail.Validation != "VALIDATION: PASS" {
		t.Errorf("legacy LoadRunDetail Validation = %q", detail.Validation)
	}
	if len(detail.Steps) != 2 {
		t.Fatalf("legacy LoadRunDetail Steps has %d entries, want 2", len(detail.Steps))
	}
}
