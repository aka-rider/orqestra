//go:build integration

package main

// WP16 QA gate (a)-(d): drives the REAL headless path (run() → buildEngine →
// Engine.Start → RunPipeline) against the replayclaude stand-in through the
// `binary` + `allow_exec` config knobs — no fake orchestrator, no test-only
// seam. This is the deterministic e2e lane the plan says replaces the
// `make test-e2e` placeholder for this package: the placeholder is about
// LIVE (real API) e2e, which is a separate, not-yet-implemented lane; this
// one exercises the full config→engine→sandbox→subprocess→pipeline path
// hermetically.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/project"
)

// wp16RepoRoot walks up from the test's working directory to the module root.
func wp16RepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above test working directory")
		}
		dir = parent
	}
}

// headlessFixture is one hermetic project directory + config wired to the
// replayclaude stand-in, ready to drive run() against.
type headlessFixture struct {
	repoDir string
	cfgPath string
}

// setupHeadlessFixture builds the replayclaude stub, drops a single-line
// stream-json recording (resultLine) as the fixture every replayed
// invocation reads, and writes a minimal-but-complete orqestra.yaml pointing
// every role's model at the stub (config.validate() requires every role's
// model to resolve — an incomplete config would hit buildEngine's internal
// os.Exit paths and take down the whole test binary, not just fail one
// test, so completeness here is load-bearing, not cosmetic).
func setupHeadlessFixture(t *testing.T, resultLine map[string]any) headlessFixture {
	t.Helper()

	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not installed (detect.DetectClaude requires it on PATH)")
	}

	repoRoot := wp16RepoRoot(t)

	binDir := t.TempDir()
	stub := filepath.Join(binDir, "replayclaude")
	build := exec.Command("go", "build", "-o", stub, "./cmd/replayclaude")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build replay stub: %v\n%s", err, out)
	}

	projectDir := t.TempDir()
	if out, err := exec.Command("git", "init", projectDir).CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", projectDir, err, out)
	}
	if err := project.Init(projectDir); err != nil {
		t.Fatalf("project.Init: %v", err)
	}

	line, err := json.Marshal(resultLine)
	if err != nil {
		t.Fatalf("marshal replay fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".orqestra-replay.ndjson"), append(line, '\n'), 0o644); err != nil {
		t.Fatalf("write replay fixture: %v", err)
	}

	cfgYAML := fmt.Sprintf(`providers:
  local:
    type: native

models:
  qwen:
    provider: local
    model: replay
    binary: %s
  small:
    provider: local
    model: replay
    binary: %s

researcher:
  model: qwen
architect:
  model: qwen
critic:
  model: qwen
worker:
  model: qwen
integrator:
  model: qwen

sandbox:
  allow_exec:
    - %s
`, stub, stub, binDir)

	cfgPath := filepath.Join(t.TempDir(), "orqestra.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	return headlessFixture{repoDir: projectDir, cfgPath: cfgPath}
}

// successResultLine is a stream-json "result" event that ReportHarvester's
// tier-3 (final message) accepts: non-empty, printable, and containing a
// markdown heading (report_sanity.go's looksLikeReport).
func successResultLine() map[string]any {
	return map[string]any{
		"type":       "result",
		"subtype":    "success",
		"is_error":   false,
		"result":     "# Plan\n\n## Goal\nReplay smoke-test plan; no real change.\n\n## Work Packages\n### 1. Noop\n**Steps:**\n1. Do nothing.\n**Done when:**\n- true\n",
		"session_id": "replay-ok-session",
		"usage":      map[string]any{"input_tokens": 120, "output_tokens": 45},
	}
}

// failureResultLine is a stream-json "result" event carrying is_error:true —
// harness.Run (exec.go) turns this into a genuine returned error, which
// ReportHarvester's tier 4 (fail closed) then propagates as the architect's
// failure (report_harvest.go).
func failureResultLine() map[string]any {
	return map[string]any{
		"type":       "result",
		"subtype":    "error_during_execution",
		"is_error":   true,
		"result":     "simulated architect failure",
		"session_id": "replay-fail-session",
	}
}

// chdirTo switches the process working directory to dir for the duration of
// the test (run() reads os.Getwd() directly, not a parameter) and restores
// the original directory via t.Cleanup.
func chdirTo(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// runBounded drives run() in a goroutine and fails the test if it does not
// return within timeout — the "must not hang" requirement for the
// gate-without-auto-approve QA gate (b). No shared-buffer read happens in
// the timeout branch, so a genuine hang cannot itself trigger a data race
// under -race on the way to failing the test.
func runBounded(t *testing.T, args []string, timeout time.Duration) (stdout, stderr string, code int) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- run(args, &outBuf, &errBuf) }()
	select {
	case code = <-done:
	case <-time.After(timeout):
		t.Fatalf("run(%v) did not return within %s — headless run hung", args, timeout)
	}
	return outBuf.String(), errBuf.String(), code
}

// TestHeadlessE2E_PlanOnlyAutoApprove_Succeeds is QA gate (a): a full
// replayclaude run with --auto-approve --plan-only exits 0, stdout contains
// a phase line and a final status line, and final_plan.md is written.
func TestHeadlessE2E_PlanOnlyAutoApprove_Succeeds(t *testing.T) {
	fx := setupHeadlessFixture(t, successResultLine())
	chdirTo(t, fx.repoDir)

	stdout, stderr, code := runBounded(t, []string{
		"orqestra", "--config", fx.cfgPath,
		"--prompt", "add a trivial no-op change",
		"--auto-approve", "--plan-only",
	}, 30*time.Second)

	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK (0)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "[phase] planning") {
		t.Errorf("stdout missing a phase line:\n%s", stdout)
	}
	if !strings.Contains(stdout, "[done] status=success") {
		t.Errorf("stdout missing final status line:\n%s", stdout)
	}

	matches, err := filepath.Glob(filepath.Join(fx.repoDir, ".orqestra", "sessions", "*", "final_plan.md"))
	if err != nil {
		t.Fatalf("glob final_plan.md: %v", err)
	}
	if len(matches) == 0 {
		t.Errorf("no .orqestra/sessions/<run>/final_plan.md written under %s", fx.repoDir)
	}
}

// TestHeadlessE2E_PlanOnly_NoExecution is QA gate (d): --plan-only writes
// final_plan.md and never runs the worker (no worker agent lines in stdout).
func TestHeadlessE2E_PlanOnly_NoExecution(t *testing.T) {
	fx := setupHeadlessFixture(t, successResultLine())
	chdirTo(t, fx.repoDir)

	stdout, stderr, code := runBounded(t, []string{
		"orqestra", "--config", fx.cfgPath,
		"--prompt", "add a trivial no-op change",
		"--auto-approve", "--plan-only",
	}, 30*time.Second)

	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK (0)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if strings.Contains(stdout, "[agent] worker") {
		t.Errorf("--plan-only must never run the worker; stdout contained a worker agent line:\n%s", stdout)
	}
	matches, err := filepath.Glob(filepath.Join(fx.repoDir, ".orqestra", "sessions", "*", "final_plan.md"))
	if err != nil {
		t.Fatalf("glob final_plan.md: %v", err)
	}
	if len(matches) == 0 {
		t.Errorf("no .orqestra/sessions/<run>/final_plan.md written under %s", fx.repoDir)
	}
}

// TestHeadlessE2E_GateWithoutAutoApprove_FailsFastAndBounded is QA gate (b):
// the default pipeline setup keeps its human gate; without --auto-approve, a
// headless run must fail fast with the explicit gate error and exit
// exitInvalidInput — never hang waiting for a human who isn't there.
func TestHeadlessE2E_GateWithoutAutoApprove_FailsFastAndBounded(t *testing.T) {
	fx := setupHeadlessFixture(t, successResultLine())
	chdirTo(t, fx.repoDir)

	stdout, stderr, code := runBounded(t, []string{
		"orqestra", "--config", fx.cfgPath,
		"--prompt", "add a trivial no-op change",
	}, 30*time.Second)

	if code != exitInvalidInput {
		t.Fatalf("exit code = %d, want exitInvalidInput (2)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "human gate requires --auto-approve or the TUI") {
		t.Errorf("stderr missing the explicit gate error:\n%s", stderr)
	}
}

// TestHeadlessE2E_FailureFixture_ExitsDomainFailure is QA gate (c): a replay
// fixture that reports is_error:true propagates through ReportHarvester's
// fail-closed tier to a StatusFailed run, mapped to exitDomainFailure.
func TestHeadlessE2E_FailureFixture_ExitsDomainFailure(t *testing.T) {
	fx := setupHeadlessFixture(t, failureResultLine())
	chdirTo(t, fx.repoDir)

	stdout, stderr, code := runBounded(t, []string{
		"orqestra", "--config", fx.cfgPath,
		"--prompt", "add a trivial no-op change",
		"--auto-approve", "--plan-only",
	}, 30*time.Second)

	if code != exitDomainFailure {
		t.Fatalf("exit code = %d, want exitDomainFailure (1)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "[done] status=failed") {
		t.Errorf("stdout missing failed status line:\n%s", stdout)
	}
}
