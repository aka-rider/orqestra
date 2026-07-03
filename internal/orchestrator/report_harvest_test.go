package orchestrator

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
)

// TestReportHarvester_SanityRejectionRecordedAndFallsThrough is the RED-first
// gate for provenance rejection tracking (WP11 gate d): a tier-1 submission
// that FAILS looksLikeReport must not be silently discarded — it must fall
// to the next tier AND the returned provenance must name tier 1
// ("submit_report") in Rejected, so a scavenge is never indistinguishable
// from a clean SubmitReport delivery.
func TestReportHarvester_SanityRejectionRecordedAndFallsThrough(t *testing.T) {
	const agentID = "architect"
	const junkSubmission = "ok thanks" // too short / no heading — fails looksLikeReport
	const validFinalMessage = "# Plan\n\n## Goal\nDo the thing.\n\n## Work Packages\n### 1. Step one"

	store := &fakeReportStore{reports: map[string]string{agentID: junkSubmission}}
	sc := StepContext{Log: slog.Default(), Reports: store}

	spec := harness.ProcessSpec{AgentID: agentID} // PlanMode false — tier 2 skipped, goes straight to tier 3
	res := harness.RunResult{Output: validFinalMessage}

	harvester := NewReportHarvester(sc, RoleReporter)
	out, prov, err := harvester.Harvest(context.Background(), spec, res, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != validFinalMessage {
		t.Errorf("expected the tier-3 final message to win, got %q", out)
	}
	if prov.Tier != 3 || prov.Source != SourceFinalMessage {
		t.Errorf("provenance = %+v, want Tier 3 / Source %q", prov, SourceFinalMessage)
	}
	found := false
	for _, r := range prov.Rejected {
		if r == SourceSubmitReport {
			found = true
		}
	}
	if !found {
		t.Errorf("provenance.Rejected = %v, want it to name %q (the sanity-failed tier-1 submission)", prov.Rejected, SourceSubmitReport)
	}
}

// TestReportHarvester_AbsentTierIsNotRecordedAsRejected proves Rejected only
// names tiers that produced TEXT that failed the sanity check — a tier that
// was simply unavailable (no submission at all) must not be conflated with
// one that was tried and failed.
func TestReportHarvester_AbsentTierIsNotRecordedAsRejected(t *testing.T) {
	const validFinalMessage = "# Plan\n\n## Goal\nDo the thing.\n\n## Work Packages\n### 1. Step one\n"
	sc := StepContext{Log: slog.Default()} // no Reports store at all — tier 1 is simply absent

	spec := harness.ProcessSpec{AgentID: "architect"}
	res := harness.RunResult{Output: validFinalMessage}

	harvester := NewReportHarvester(sc, RoleReporter)
	_, prov, err := harvester.Harvest(context.Background(), spec, res, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(prov.Rejected) != 0 {
		t.Errorf("provenance.Rejected = %v, want empty (tier 1 was never attempted, not rejected)", prov.Rejected)
	}
}

// TestReportHarvester_PlanFileChanged_MtimeSizeDelta directly exercises the
// J35 freshness check (WP11 gate b) with real temp files and controlled
// mtimes, independent of any Step — SnapshotPlanFile before a simulated
// rewrite, then Harvest after.
func TestReportHarvester_PlanFileChanged_MtimeSizeDelta(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	plansDir := filepath.Join(tmp, ".claude", "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("unchanged plan file is not accepted at tier 2", func(t *testing.T) {
		planFile := filepath.Join(plansDir, "unchanged.md")
		content := "# Plan\n\n## Goal\nOriginal.\n\n## Work Packages\n### 1. X\n"
		if err := os.WriteFile(planFile, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		sc := StepContext{Log: slog.Default(), RepoPath: t.TempDir()}
		harvester := NewReportHarvester(sc, RoleReporter)
		harvester.SnapshotPlanFile("sess-1", planFile, sc.RepoPath)

		// Nothing rewrites the file between snapshot and harvest.
		spec := harness.ProcessSpec{AgentID: "architect", PlanMode: true}
		freshMessage := "# Plan\n\n## Goal\nFresh chat response.\n\n## Work Packages\n### 1. Y"
		res := harness.RunResult{SessionID: "sess-1", PlanFilePath: planFile, Output: freshMessage}

		out, prov, err := harvester.Harvest(context.Background(), spec, res, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if prov.Source == SourcePlanFile {
			t.Errorf("tier 2 (plan file) must NOT win when the file is unchanged; got Source=%q, Detail=%q", prov.Source, prov.Detail)
		}
		if out != freshMessage {
			t.Errorf("expected the fresh final message to win, got %q", out)
		}
	})

	t.Run("changed plan file wins tier 2", func(t *testing.T) {
		planFile := filepath.Join(plansDir, "changed.md")
		staleContent := "# Plan\n\n## Goal\nOriginal.\n\n## Work Packages\n### 1. X\n"
		if err := os.WriteFile(planFile, []byte(staleContent), 0o644); err != nil {
			t.Fatal(err)
		}

		sc := StepContext{Log: slog.Default(), RepoPath: t.TempDir()}
		harvester := NewReportHarvester(sc, RoleReporter)
		harvester.SnapshotPlanFile("sess-2", planFile, sc.RepoPath)

		// Simulate the agent rewriting the plan file during the invocation.
		freshContent := "# Plan\n\n## Goal\nRewritten.\n\n## Work Packages\n### 1. X\n### 2. Y\n"
		if err := os.WriteFile(planFile, []byte(freshContent), 0o644); err != nil {
			t.Fatal(err)
		}

		spec := harness.ProcessSpec{AgentID: "architect", PlanMode: true}
		res := harness.RunResult{SessionID: "sess-2", PlanFilePath: planFile, Output: "short chat, no heading"}

		out, prov, err := harvester.Harvest(context.Background(), spec, res, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if prov.Tier != 2 || prov.Source != SourcePlanFile {
			t.Errorf("provenance = %+v, want Tier 2 / Source %q", prov, SourcePlanFile)
		}
		if prov.Detail == freshnessUnverified {
			t.Error("a genuinely compared, changed file must not be labeled freshness-unverified")
		}
		wantTrimmed := "# Plan\n\n## Goal\nRewritten.\n\n## Work Packages\n### 1. X\n### 2. Y"
		if out != wantTrimmed {
			t.Errorf("out = %q, want %q", out, wantTrimmed)
		}
	})

	t.Run("no prior snapshot is freshness-unverified, not silently wrong", func(t *testing.T) {
		planFile := filepath.Join(plansDir, "first-ever.md")
		content := "# Plan\n\n## Goal\nFirst plan ever written.\n\n## Work Packages\n### 1. X"
		if err := os.WriteFile(planFile, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}

		sc := StepContext{Log: slog.Default(), RepoPath: t.TempDir()}
		harvester := NewReportHarvester(sc, RoleReporter)
		// No SnapshotPlanFile call at all — mirrors the initial architect pass,
		// where no prior session/plan exists to compare against.

		spec := harness.ProcessSpec{AgentID: "architect", PlanMode: true}
		res := harness.RunResult{SessionID: "sess-3", PlanFilePath: planFile, Output: "irrelevant chat text"}

		out, prov, err := harvester.Harvest(context.Background(), spec, res, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if prov.Tier != 2 || prov.Source != SourcePlanFile {
			t.Errorf("provenance = %+v, want Tier 2 / Source %q (accepted as today, but labeled)", prov, SourcePlanFile)
		}
		if prov.Detail != freshnessUnverified {
			t.Errorf("Detail = %q, want %q — must be truthful that freshness was never proven", prov.Detail, freshnessUnverified)
		}
		if out != content {
			t.Errorf("out = %q, want %q", out, content)
		}
	})
}

// TestReportHarvester_Executor_PrefersSubmitReportOverRawOutput is WP11 gate
// (e): the worker's tier order is SubmitReport → raw output, with no sanity
// check on raw output (it is arbitrary work output, not a structured
// report).
func TestReportHarvester_Executor_PrefersSubmitReportOverRawOutput(t *testing.T) {
	const agentID = "worker"
	store := &fakeReportStore{reports: map[string]string{agentID: "the worker's SubmitReport text"}}
	sc := StepContext{Log: slog.Default(), Reports: store}

	spec := harness.ProcessSpec{AgentID: agentID}
	res := harness.RunResult{SessionID: "worker-sid", Output: "raw stdout the worker printed"}

	harvester := NewReportHarvester(sc, RoleExecutor)
	out, prov, err := harvester.Harvest(context.Background(), spec, res, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "the worker's SubmitReport text" {
		t.Errorf("out = %q, want the SubmitReport text", out)
	}
	if prov.Tier != 1 || prov.Source != SourceSubmitReport {
		t.Errorf("provenance = %+v, want Tier 1 / Source %q", prov, SourceSubmitReport)
	}
}

// TestReportHarvester_Executor_FallsBackToRawOutput proves the worker path
// falls back to res.Output, unchecked, when no SubmitReport arrived —
// including when Output happens not to look like a structured report at
// all (raw output is never sanity-checked).
func TestReportHarvester_Executor_FallsBackToRawOutput(t *testing.T) {
	sc := StepContext{Log: slog.Default()} // no Reports store

	spec := harness.ProcessSpec{AgentID: "worker"}
	res := harness.RunResult{SessionID: "worker-sid-2", Output: "created foo.go, ran go build, all green"}

	harvester := NewReportHarvester(sc, RoleExecutor)
	out, prov, err := harvester.Harvest(context.Background(), spec, res, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != res.Output {
		t.Errorf("out = %q, want raw output %q", out, res.Output)
	}
	if prov.Source != SourceRawOutput {
		t.Errorf("provenance.Source = %q, want %q", prov.Source, SourceRawOutput)
	}
}

// TestReportHarvester_SnapshotPlanFile_NoSessionIsNoPriorState proves
// SnapshotPlanFile("", ...) never establishes a pre-invocation snapshot —
// the caller has no prior session to compare against (the very first
// architect pass).
func TestReportHarvester_SnapshotPlanFile_NoSessionIsNoPriorState(t *testing.T) {
	sc := StepContext{Log: slog.Default(), RepoPath: t.TempDir()}
	harvester := NewReportHarvester(sc, RoleReporter)
	harvester.SnapshotPlanFile("", "", sc.RepoPath)
	if harvester.havePreSnapshot {
		t.Error("SnapshotPlanFile with an empty session ID must not establish a pre-invocation snapshot")
	}
}

// TestReportHarvester_PlanFileChanged_DifferentPathIsChanged proves that a
// prior and post path that simply DIFFER (not merely differ in mtime/size)
// is treated as changed — a definitively fresh write, not an unverified one.
func TestReportHarvester_PlanFileChanged_DifferentPathIsChanged(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	plansDir := filepath.Join(tmp, ".claude", "plans")
	if err := os.MkdirAll(plansDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(plansDir, "old-session-plan.md")
	newPath := filepath.Join(plansDir, "new-session-plan.md")
	if err := os.WriteFile(oldPath, []byte("# Plan\nOld"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("# Plan\nNew"), 0o644); err != nil {
		t.Fatal(err)
	}

	sc := StepContext{Log: slog.Default(), RepoPath: t.TempDir()}
	harvester := NewReportHarvester(sc, RoleReporter)
	harvester.SnapshotPlanFile("sess-old", oldPath, sc.RepoPath)

	changed, unverified := harvester.planFileChanged(harness.RunResult{SessionID: "sess-new", PlanFilePath: newPath})
	if !changed {
		t.Error("a different plan file path must be treated as changed")
	}
	if unverified {
		t.Error("a definitively different path is not 'unverified' — it IS a change")
	}
}

// TestReportHarvester_PlanFileTierErrorRecorded is WP18's A5 QA gate: a tier
// that ERRORS while being retrieved (as opposed to merely failing the sanity
// check) must leave a trace in ReportProvenance.Errored, not vanish silently.
// Here tier 2 (plan file) errors because res.SessionID is empty — ReadPlanFile
// fails closed with "no session ID" — while tier 3 (final message, from
// res.Output) succeeds, so Harvest still returns a usable report.
//
// RED-first: against the pre-A5 harvestReporter (the `if plan, err := ...;
// err == nil { ... }` tier-2 branch with no else), this test failed with
// "provenance.Errored = [], want an entry naming \"plan_file\"" — the tier-2
// read error left no trace at all.
func TestReportHarvester_PlanFileTierErrorRecorded(t *testing.T) {
	const validFinalMessage = "# Plan\n\n## Goal\nDo the thing.\n\n## Work Packages\n### 1. Step one"
	sc := StepContext{Log: slog.Default(), RepoPath: t.TempDir()}

	spec := harness.ProcessSpec{AgentID: "architect", PlanMode: true}
	res := harness.RunResult{SessionID: "", Output: validFinalMessage} // no session ID -> preferReport errors

	harvester := NewReportHarvester(sc, RoleReporter)
	out, prov, err := harvester.Harvest(context.Background(), spec, res, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != validFinalMessage {
		t.Errorf("out = %q, want the tier-3 fallback %q", out, validFinalMessage)
	}
	if prov.Tier != 3 {
		t.Errorf("provenance.Tier = %d, want 3 (fallen through from the errored plan-file tier)", prov.Tier)
	}
	found := false
	for _, e := range prov.Errored {
		if strings.HasPrefix(e, SourcePlanFile+":") {
			found = true
		}
	}
	if !found {
		t.Errorf("provenance.Errored = %v, want an entry naming %q (the plan-file tier's read error)", prov.Errored, SourcePlanFile)
	}
}

// TestReportHarvester_TerminalErrorNamesErroredTiers proves the fail-closed
// tier-4 error names every tier that errored on the way there — not just
// which ones failed the sanity check — so a debugging session can tell "the
// plan file couldn't be read" apart from "the plan file read fine but looked
// like junk".
func TestReportHarvester_TerminalErrorNamesErroredTiers(t *testing.T) {
	sc := StepContext{Log: slog.Default(), RepoPath: t.TempDir()}
	spec := harness.ProcessSpec{AgentID: "architect", PlanMode: true}
	res := harness.RunResult{} // no session, no output: both plan-file and final-message tiers error

	harvester := NewReportHarvester(sc, RoleReporter)
	_, prov, err := harvester.Harvest(context.Background(), spec, res, nil)
	if err == nil {
		t.Fatal("expected a fail-closed error when every tier errors or is absent")
	}
	if !strings.Contains(err.Error(), SourcePlanFile) {
		t.Errorf("terminal error %q does not name the errored %q tier", err.Error(), SourcePlanFile)
	}
	if !strings.Contains(err.Error(), SourceFinalMessage) {
		t.Errorf("terminal error %q does not name the errored %q tier", err.Error(), SourceFinalMessage)
	}
	if len(prov.Errored) != 2 {
		t.Errorf("prov.Errored = %v, want 2 entries (plan_file, final_message)", prov.Errored)
	}
}
