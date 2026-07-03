package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

// TestWP14_GoldenSpecs is the WP14/RC4 falsifiability gate (RED-first):
// testdata/wp14_golden.json was captured by running this SAME fixture
// config through buildEngine on the pre-refactor tree (commit 6c86f54,
// before ClaudeCLI/ClaudeCLIOption/BuildProcessSpec were deleted and
// harness.SpecForRole/config.Config.Roles() were introduced) — see the WP14
// worker report for the exact baseline capture command and raw output. This
// test reproduces the same six specs through the POST-refactor buildEngine
// and asserts byte-identical (args/env/knobs) equality. Args are normalized
// first: the "orqestra" inline MCP bridge server's command/socket embed the
// running test binary's own path and PID, which vary run to run — both the
// baseline capture and this comparison replace them with fixed placeholders
// before comparing (see normalizeArgs).
type goldenSpec struct {
	Args            []string
	Model           harness.ModelSpec
	SandboxRepoPath string
	SandboxWorktree string
	SandboxWritable bool
	SandboxEnv      []string
	AgentID         string
	ExpectsReport   bool
	PlanMode        bool
	InputPlane      bool
	Timeout         time.Duration
	LoopGuard       harness.LoopGuardSpec
	SilenceGuard    harness.SilenceGuardSpec
	PreTimeoutNudge string
}

// normalizeArgs replaces the two per-process-run dynamic values buildEngine
// embeds into the "orqestra" inline MCP server's args (the test binary's own
// path from os.Executable, and the PID-based socket path) with fixed
// placeholders, so the golden fixture is stable across runs/machines/PIDs.
func normalizeArgs(t *testing.T, args []string) []string {
	t.Helper()
	selfBin, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	socketPath := fmt.Sprintf("/tmp/orqestra-q-%d.sock", os.Getpid())
	out := make([]string, len(args))
	for i, a := range args {
		a = strings.ReplaceAll(a, selfBin, "<SELF_BIN>")
		a = strings.ReplaceAll(a, socketPath, "<SOCKET>")
		out[i] = a
	}
	return out
}

func captureGolden(t *testing.T, spec harness.ProcessSpec) goldenSpec {
	t.Helper()
	env := append([]string(nil), spec.Sandbox.Env...)
	sort.Strings(env)
	args, err := harness.SpecArgs(spec)
	if err != nil {
		t.Fatalf("SpecArgs: %v", err)
	}
	return goldenSpec{
		Args:            normalizeArgs(t, args),
		Model:           spec.Model,
		SandboxRepoPath: spec.Sandbox.RepoPath,
		SandboxWorktree: spec.Sandbox.WorktreePath,
		SandboxWritable: spec.Sandbox.Writable,
		SandboxEnv:      env,
		AgentID:         spec.AgentID,
		ExpectsReport:   spec.ExpectsReport,
		PlanMode:        spec.PlanMode,
		InputPlane:      spec.InputPlane,
		Timeout:         spec.Timeout,
		LoopGuard:       spec.LoopGuard,
		SilenceGuard:    spec.SilenceGuard,
		PreTimeoutNudge: spec.PreTimeoutNudge,
	}
}

func buildGoldenEngine(t *testing.T) *goldenEngineResult {
	t.Helper()
	cfg, err := config.Load(filepath.Join("testdata", "wp14_golden.yaml"))
	if err != nil {
		t.Fatalf("load fixture config: %v", err)
	}
	engine := buildEngine(cfg, nil, "/fixture/repo")

	conflictSpec, err := engine.Specs.IntegratorConflictSpecFn("/fixture/worktree")
	if err != nil {
		t.Fatalf("integrator conflict spec: %v", err)
	}

	return &goldenEngineResult{
		architect:          engine.Specs.Architect,
		critic:             engine.Specs.Critic,
		worker:             engine.Specs.Worker,
		worktree:           engine.Specs.WorktreeSpecFn("/fixture/worktree"),
		integratorCommit:   engine.Specs.Integrator,
		integratorConflict: conflictSpec,
	}
}

type goldenEngineResult struct {
	architect          harness.ProcessSpec
	critic             harness.ProcessSpec
	worker             harness.ProcessSpec
	worktree           harness.ProcessSpec
	integratorCommit   harness.ProcessSpec
	integratorConflict harness.ProcessSpec
}

// TestWP14_GoldenSpecs_PostRefactor is the WP14 golden gate: it MUST pass
// unchanged (byte-identical args/env/knobs) against the pre-refactor capture
// in testdata/wp14_golden.json.
func TestWP14_GoldenSpecs_PostRefactor(t *testing.T) {
	specs := buildGoldenEngine(t)

	got := map[string]goldenSpec{
		"architect":           captureGolden(t, specs.architect),
		"critic":              captureGolden(t, specs.critic),
		"worker":              captureGolden(t, specs.worker),
		"worktree":            captureGolden(t, specs.worktree),
		"integrator_commit":   captureGolden(t, specs.integratorCommit),
		"integrator_conflict": captureGolden(t, specs.integratorConflict),
	}

	raw, err := os.ReadFile(filepath.Join("testdata", "wp14_golden.json"))
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	var want map[string]goldenSpec
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("unmarshal golden fixture: %v", err)
	}

	if len(want) != len(got) {
		t.Fatalf("golden fixture has %d specs, captured %d: fixture=%v captured=%v", len(want), len(got), keysOf(want), keysOf(got))
	}

	for name, wantSpec := range want {
		gotSpec, ok := got[name]
		if !ok {
			t.Errorf("golden fixture names %q but no such spec was captured", name)
			continue
		}
		if !reflect.DeepEqual(gotSpec, wantSpec) {
			gj, _ := json.MarshalIndent(gotSpec, "", "  ")
			wj, _ := json.MarshalIndent(wantSpec, "", "  ")
			t.Errorf("golden mismatch for %q:\n--- got ---\n%s\n--- want ---\n%s", name, gj, wj)
		}
	}
}

func keysOf(m map[string]goldenSpec) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
