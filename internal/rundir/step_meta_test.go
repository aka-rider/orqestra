package rundir

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveStepMeta_LoadStepMetas_RoundTrip(t *testing.T) {
	dir := Dir{Path: t.TempDir()}
	base := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)

	metas := []struct {
		role string
		m    StepMeta
	}{
		{"architect", StepMeta{AgentID: "architect", StartTime: base, EndTime: base.Add(time.Minute), Status: "done", InputTokens: 10, OutputTokens: 20}},
		{"critic_round1", StepMeta{AgentID: "critic", StartTime: base.Add(2 * time.Minute), EndTime: base.Add(3 * time.Minute), Status: "done"}},
		{"worker", StepMeta{AgentID: "worker", StartTime: base.Add(4 * time.Minute), EndTime: base.Add(5 * time.Minute), Status: "done", ClaudeSessionLogPath: "/tmp/worker.jsonl"}},
	}
	for _, entry := range metas {
		if err := dir.SaveStepMeta(entry.role, entry.m); err != nil {
			t.Fatalf("SaveStepMeta(%q): %v", entry.role, err)
		}
	}

	loaded, err := dir.LoadStepMetas()
	if err != nil {
		t.Fatalf("LoadStepMetas: %v", err)
	}
	if len(loaded) != 3 {
		t.Fatalf("LoadStepMetas returned %d metas, want 3", len(loaded))
	}
	// Ordering: sorted by StartTime ascending, so architect, critic, worker.
	wantOrder := []string{"architect", "critic", "worker"}
	for i, want := range wantOrder {
		if loaded[i].AgentID != want {
			t.Errorf("loaded[%d].AgentID = %q, want %q", i, loaded[i].AgentID, want)
		}
	}
	if loaded[2].ClaudeSessionLogPath != "/tmp/worker.jsonl" {
		t.Errorf("worker meta ClaudeSessionLogPath = %q, want /tmp/worker.jsonl", loaded[2].ClaudeSessionLogPath)
	}
}

// TestLoadStepMetas_Determinism proves ties on StartTime break on AgentID,
// then filename — repeated loads must return an identical order (§1.7).
func TestLoadStepMetas_Determinism(t *testing.T) {
	dir := Dir{Path: t.TempDir()}
	same := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)

	// Same StartTime, different AgentIDs — must sort by AgentID.
	if err := dir.SaveStepMeta("zeta_role", StepMeta{AgentID: "zeta", StartTime: same}); err != nil {
		t.Fatal(err)
	}
	if err := dir.SaveStepMeta("alpha_role", StepMeta{AgentID: "alpha", StartTime: same}); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 5; i++ {
		loaded, err := dir.LoadStepMetas()
		if err != nil {
			t.Fatalf("LoadStepMetas (iteration %d): %v", i, err)
		}
		if len(loaded) != 2 {
			t.Fatalf("iteration %d: got %d metas, want 2", i, len(loaded))
		}
		if loaded[0].AgentID != "alpha" || loaded[1].AgentID != "zeta" {
			t.Fatalf("iteration %d: order = [%s, %s], want [alpha, zeta]", i, loaded[0].AgentID, loaded[1].AgentID)
		}
	}
}

// TestLoadStepMetas_LegacyLayoutStillLoads hand-writes *_meta.json files in
// today's flat naming (no call to SaveStepMeta) and proves LoadStepMetas
// still discovers and parses them — proving old runs keep loading (WP15).
func TestLoadStepMetas_LegacyLayoutStillLoads(t *testing.T) {
	root := t.TempDir()
	writeJSON := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON("architect_meta.json", `{
  "agent_id": "architect",
  "start_time": "2026-07-02T12:00:00Z",
  "end_time": "2026-07-02T12:01:00Z",
  "status": "done",
  "input_tokens": 100,
  "output_tokens": 200
}`)
	writeJSON("worker_meta.json", `{
  "agent_id": "worker",
  "start_time": "2026-07-02T12:02:00Z",
  "end_time": "2026-07-02T12:03:00Z",
  "status": "done"
}`)
	// A foreign meta shape (integrator_meta.json has never had agent_id/
	// start_time fields) — must not break the scan.
	writeJSON("integrator_meta.json", `{"base_pre_sha":"abc123","give_up_reason":"resolve_conflicts=false"}`)

	dir := Dir{Path: root}
	loaded, err := dir.LoadStepMetas()
	if err != nil {
		t.Fatalf("LoadStepMetas on legacy layout: %v", err)
	}
	if len(loaded) != 3 {
		t.Fatalf("LoadStepMetas on legacy layout returned %d metas, want 3 (incl. the foreign-shaped one)", len(loaded))
	}
	foundArchitect, foundWorker := false, false
	for _, m := range loaded {
		switch m.AgentID {
		case "architect":
			foundArchitect = true
			if m.InputTokens != 100 || m.OutputTokens != 200 {
				t.Errorf("architect meta tokens = %d/%d, want 100/200", m.InputTokens, m.OutputTokens)
			}
		case "worker":
			foundWorker = true
		}
	}
	if !foundArchitect || !foundWorker {
		t.Errorf("legacy metas missing: architect=%v worker=%v", foundArchitect, foundWorker)
	}
}

func TestLoadStepMetas_EmptyDirReturnsNilNotError(t *testing.T) {
	dir := Dir{Path: t.TempDir()}
	loaded, err := dir.LoadStepMetas()
	if err != nil {
		t.Fatalf("LoadStepMetas on empty dir: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("LoadStepMetas on empty dir returned %d metas, want 0", len(loaded))
	}
}
