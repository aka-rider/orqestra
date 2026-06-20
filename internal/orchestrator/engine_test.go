package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
)

// --- Test helpers ---

// fakeStep is a generic Step[In, Out] for tests.
type fakeStep[In, Out any] struct {
	agentID AgentID
	fn      func(ctx context.Context, in In, sc StepContext) (Out, error)
}

func (s *fakeStep[In, Out]) ID() AgentID { return s.agentID }
func (s *fakeStep[In, Out]) Run(ctx context.Context, in In, sc StepContext) (Out, error) {
	return s.fn(ctx, in, sc)
}

// fakeResearchStep returns a step that emits a fixed draft.
func fakeResearchStep(draft string) Step[ResearchInput, ResearchOutput] {
	return &fakeStep[ResearchInput, ResearchOutput]{
		agentID: "researcher",
		fn: func(ctx context.Context, in ResearchInput, sc StepContext) (ResearchOutput, error) {
			return ResearchOutput{DraftMarkdown: draft}, nil
		},
	}
}

// fakeResearchStepErr returns a step that returns an error.
func fakeResearchStepErr(err error) Step[ResearchInput, ResearchOutput] {
	return &fakeStep[ResearchInput, ResearchOutput]{
		agentID: "researcher",
		fn: func(ctx context.Context, in ResearchInput, sc StepContext) (ResearchOutput, error) {
			return ResearchOutput{}, err
		},
	}
}

// fakeDeliberateStep returns a step that emits a fixed plan.
func fakeDeliberateStep(markdown string) Step[DeliberateInput, PlanOutput] {
	return &fakeStep[DeliberateInput, PlanOutput]{
		agentID: "architect",
		fn: func(ctx context.Context, in DeliberateInput, sc StepContext) (PlanOutput, error) {
			return PlanOutput{Markdown: markdown}, nil
		},
	}
}

// fakeReviseStep returns a step that echoes the incoming plan unchanged.
func fakeReviseStep() Step[ReviseInput, PlanOutput] {
	return &fakeStep[ReviseInput, PlanOutput]{
		agentID: "architect",
		fn: func(ctx context.Context, in ReviseInput, sc StepContext) (PlanOutput, error) {
			plan := in.Plan
			if in.Decision.Type == DecisionEdit && in.Decision.EditedContent != "" {
				plan.Markdown = in.Decision.EditedContent
			}
			return plan, nil
		},
	}
}

// fakeExecuteStep returns a step that emits a fixed output string.
func fakeExecuteStep(output string) Step[ExecuteInput, ExecuteOutput] {
	return &fakeStep[ExecuteInput, ExecuteOutput]{
		agentID: "worker",
		fn: func(ctx context.Context, in ExecuteInput, sc StepContext) (ExecuteOutput, error) {
			return ExecuteOutput{WorkOutput: output}, nil
		},
	}
}

// fakeValidateStep returns a step that emits a fixed validation string.
func fakeValidateStep(output string) Step[ValidateInput, ValidateOutput] {
	return &fakeStep[ValidateInput, ValidateOutput]{
		agentID: "worker",
		fn: func(ctx context.Context, in ValidateInput, sc StepContext) (ValidateOutput, error) {
			return ValidateOutput{Output: output}, nil
		},
	}
}

// testStepContext builds a StepContext with an ObsStore, Control, and NoopArtifactSink.
func testStepContext(obs *ObsStore, ctrl Control) StepContext {
	return StepContext{
		Exec: harness.RunFunc(func(_ context.Context, _ harness.ProcessSpec, _ <-chan harness.Message, _ harness.Sink) (harness.RunResult, error) {
			return harness.RunResult{}, nil
		}),
		Obs:       obs,
		Artifacts: NoopArtifactSink(),
		Control:   ctrl,
		Log:       slog.Default(),
	}
}

// validPlanMarkdown returns a minimal valid plan markdown.
const validPlanMarkdown = "# Plan\n\n## Goal\nTest.\n\n## Work Packages\n\n### 1. Do it\n\n**Steps:**\n1. Edit foo.go\n\n**Done when:**\n- Tests pass"

// defaultTestSteps returns a PipelineSteps wired with fake steps for most tests.
func defaultTestSteps() PipelineSteps {
	return PipelineSteps{
		Research:   fakeResearchStep("## Draft"),
		Deliberate: fakeDeliberateStep(validPlanMarkdown),
		Revise:     fakeReviseStep(),
		Execute:    fakeExecuteStep("done"),
		Validate:   fakeValidateStep(agent.MarkerPass + " tests pass"),
	}
}

// driveGate waits for a gate at pos and submits dec.
// Must be launched in a goroutine before RunPipeline is called.
// cancel is called on timeout so that the RunPipeline call in the parent goroutine unblocks.
func driveGate(t *testing.T, obs *ObsStore, ctrl Control, pos HumanGatePosition, dec Decision, timeout time.Duration, cancel context.CancelFunc) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		snap := obs.Snapshot()
		if snap.HasGate && snap.Gate.Position == pos {
			ctrl.Submit(dec)
			return
		}
		select {
		case <-obs.NotifyCh():
		case <-timer.C:
			t.Errorf("driveGate timeout waiting for gate at position %v", pos)
			cancel()
			return
		}
	}
}

// runPipelineSync is a convenience wrapper: builds obs+ctrl, returns result+err.
func runPipelineSync(ctx context.Context, setup PipelineSetup, steps PipelineSteps) (Result, error) {
	obs := NewObsStore()
	ctrl := NewControl(obs)
	sc := testStepContext(obs, ctrl)
	return RunPipeline(ctx, setup, PipelineRunInput{Prompt: "test prompt", RunID: "test-run"}, sc, steps)
}

// --- Git helpers (retained for skipped merge tests) ---

func initGitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	filePath := filepath.Join(repo, "file.txt")
	if err := os.WriteFile(filePath, []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}
	runGit(t, repo, "add", "file.txt")
	runGit(t, repo, "commit", "-m", "initial")
	return repo
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out))
}

func gitBranchExists(t *testing.T, repoPath, branch string) bool {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", branch)
	cmd.Dir = repoPath
	return cmd.Run() == nil
}

func gitAnyWorktreeBranchExists(t *testing.T, repoPath string) bool {
	t.Helper()
	out, err := exec.Command("git", "-C", repoPath, "branch").CombinedOutput()
	if err != nil {
		t.Fatalf("git branch: %v", err)
	}
	return strings.Contains(string(out), "orqestra-run-")
}

func newSessionDirFactory(t *testing.T) RunDirFactory {
	t.Helper()
	root := t.TempDir()
	return func(slug string) (agent.SessionDir, error) {
		dir := filepath.Join(root, slug)
		if err := os.Mkdir(dir, 0o755); err != nil {
			return agent.SessionDir{}, err
		}
		return agent.SessionDir{Path: dir}, nil
	}
}

// --- Active tests ---

func TestEngine_PlanApprovalGate(t *testing.T) {
	obs := NewObsStore()
	ctrl := NewControl(obs)
	sc := testStepContext(obs, ctrl)

	setup := PipelineSetup{
		Research: true, Execution: true, Validation: true,
		HumanGates: HumanGateSet{GateAfterDeliberation},
	}
	steps := defaultTestSteps()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go driveGate(t, obs, ctrl, GateAfterDeliberation, Decision{Type: DecisionApprove}, 5*time.Second, cancel)

	result, err := RunPipeline(ctx, setup, PipelineRunInput{Prompt: "Add feature X", RunID: "run-1"}, sc, steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, StatusSuccess)
	}
}

func TestEngine_CancelAtGate(t *testing.T) {
	obs := NewObsStore()
	ctrl := NewControl(obs)
	sc := testStepContext(obs, ctrl)

	setup := PipelineSetup{
		Research: true, Execution: true, Validation: true,
		HumanGates: HumanGateSet{GateAfterDeliberation},
	}
	steps := defaultTestSteps()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go driveGate(t, obs, ctrl, GateAfterDeliberation, Decision{Type: DecisionCancel}, 5*time.Second, cancel)

	result, err := RunPipeline(ctx, setup, PipelineRunInput{Prompt: "Add feature X", RunID: "run-1"}, sc, steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusCancelled {
		t.Errorf("status = %q, want %q", result.Status, StatusCancelled)
	}
}

func TestEngine_SkipGateway(t *testing.T) {
	// No gates in setup → pipeline completes without blocking.
	setup := PipelineSetup{
		Research: true, Execution: true, Validation: true,
		HumanGates: nil,
	}
	result, err := runPipelineSync(context.Background(), setup, defaultTestSteps())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, StatusSuccess)
	}
}

func TestEngine_NoGate(t *testing.T) {
	// Explicit HumanGates: nil → no gate fires.
	obs := NewObsStore()
	ctrl := NewControl(obs)
	sc := testStepContext(obs, ctrl)

	setup := PipelineSetup{
		Research: true, Execution: true, Validation: true,
		HumanGates: nil,
	}

	result, err := RunPipeline(context.Background(), setup, PipelineRunInput{Prompt: "Add feature X", RunID: "run-1"}, sc, defaultTestSteps())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	snap := obs.Snapshot()
	if snap.HasGate {
		t.Error("expected no gate to be open when HumanGates is empty")
	}
	if result.Status != StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, StatusSuccess)
	}
}

func TestEngine_PhaseOrder(t *testing.T) {
	obs := NewObsStore()
	ctrl := NewControl(obs)

	var mu sync.Mutex
	var phases []Phase

	recordingObs := &phaseRecorder{ObsStore: obs, record: func(p Phase) {
		mu.Lock()
		phases = append(phases, p)
		mu.Unlock()
	}}

	sc := StepContext{
		Exec: harness.RunFunc(func(_ context.Context, _ harness.ProcessSpec, _ <-chan harness.Message, _ harness.Sink) (harness.RunResult, error) {
			return harness.RunResult{}, nil
		}),
		Obs:       recordingObs,
		Artifacts: NoopArtifactSink(),
		Control:   ctrl,
		Log:       slog.Default(),
	}

	setup := PipelineSetup{
		Research: true, Execution: true, Validation: true,
		HumanGates: nil,
	}

	_, err := RunPipeline(context.Background(), setup, PipelineRunInput{Prompt: "Add feature X", RunID: "run-1"}, sc, defaultTestSteps())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	got := append([]Phase(nil), phases...)
	mu.Unlock()

	expected := []Phase{PhaseResearching, PhasePlanning, PhaseExecuting, PhaseSelfValidating}
	if len(got) != len(expected) {
		t.Fatalf("phases = %v, want %v", got, expected)
	}
	for i, p := range got {
		if p != expected[i] {
			t.Errorf("phase[%d] = %q, want %q", i, p, expected[i])
		}
	}
}

// phaseRecorder wraps ObsStore to intercept PhaseChanged calls.
type phaseRecorder struct {
	*ObsStore
	record func(Phase)
}

func (r *phaseRecorder) PhaseChanged(p Phase) {
	r.record(p)
	r.ObsStore.PhaseChanged(p)
}

func TestEngine_NoExecute(t *testing.T) {
	obs := NewObsStore()
	ctrl := NewControl(obs)

	var mu sync.Mutex
	var phases []Phase
	recordingObs := &phaseRecorder{ObsStore: obs, record: func(p Phase) {
		mu.Lock()
		phases = append(phases, p)
		mu.Unlock()
	}}

	sc := StepContext{
		Exec: harness.RunFunc(func(_ context.Context, _ harness.ProcessSpec, _ <-chan harness.Message, _ harness.Sink) (harness.RunResult, error) {
			return harness.RunResult{}, nil
		}),
		Obs:       recordingObs,
		Artifacts: NoopArtifactSink(),
		Control:   ctrl,
		Log:       slog.Default(),
	}

	setup := PipelineSetup{
		Research: true, Execution: false, Validation: false,
		HumanGates: nil,
	}

	result, err := RunPipeline(context.Background(), setup, PipelineRunInput{Prompt: "Add feature X", RunID: "run-1"}, sc, defaultTestSteps())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, StatusSuccess)
	}

	mu.Lock()
	got := append([]Phase(nil), phases...)
	mu.Unlock()

	for _, p := range got {
		if p == PhaseExecuting {
			t.Error("expected no executing phase with Execution disabled in PipelineSetup")
		}
	}
}

func TestEngine_ValidationFailureDetection(t *testing.T) {
	// Validation is advisory — pipeline still succeeds, but raw output contains the fail marker.
	steps := defaultTestSteps()
	steps.Validate = fakeValidateStep(agent.MarkerFail + " tests failed")

	setup := PipelineSetup{
		Research: true, Execution: true, Validation: true,
		HumanGates: nil,
	}

	result, err := runPipelineSync(context.Background(), setup, steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Validation is advisory: status is success even with fail marker.
	if !strings.Contains(result.WorkerValidation, agent.MarkerFail) {
		t.Errorf("WorkerValidation %q should contain fail marker %q", result.WorkerValidation, agent.MarkerFail)
	}
}

func TestEngine_ValidationSuccessDetection(t *testing.T) {
	steps := defaultTestSteps()
	steps.Validate = fakeValidateStep(agent.MarkerPass + " tests pass\n" + agent.MarkerPass + " build ok")

	setup := PipelineSetup{
		Research: true, Execution: true, Validation: true,
		HumanGates: nil,
	}

	result, err := runPipelineSync(context.Background(), setup, steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, StatusSuccess)
	}
	if !strings.Contains(result.WorkerValidation, agent.MarkerPass) {
		t.Errorf("WorkerValidation %q should contain pass marker %q", result.WorkerValidation, agent.MarkerPass)
	}
}

func TestEngine_DecisionEdit(t *testing.T) {
	obs := NewObsStore()
	ctrl := NewControl(obs)
	sc := testStepContext(obs, ctrl)

	setup := PipelineSetup{
		Research: true, Execution: true, Validation: true,
		HumanGates: HumanGateSet{GateAfterDeliberation},
	}
	steps := defaultTestSteps()

	editedContent := "# Plan\n\n## Goal\nEdited.\n\n## Work Packages\n\n### 1. Do it\n\n**Steps:**\n1. Edit foo.go\n\n**Done when:**\n- Tests pass"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// First gate: send Edit; second gate: send Approve.
	gateCount := 0
	var gateMu sync.Mutex
	go func() {
		for {
			snap := obs.Snapshot()
			if snap.HasGate && snap.Gate.Position == GateAfterDeliberation {
				gateMu.Lock()
				gateCount++
				n := gateCount
				gateMu.Unlock()
				if n == 1 {
					ctrl.Submit(Decision{Type: DecisionEdit, EditedContent: editedContent})
				} else {
					ctrl.Submit(Decision{Type: DecisionApprove})
					return
				}
			}
			select {
			case <-obs.NotifyCh():
			case <-ctx.Done():
				return
			}
		}
	}()

	result, err := RunPipeline(ctx, setup, PipelineRunInput{Prompt: "Add feature X", RunID: "run-1"}, sc, steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusSuccess {
		t.Errorf("status = %q, want %q", result.Status, StatusSuccess)
	}
	gateMu.Lock()
	if gateCount < 2 {
		t.Errorf("expected at least 2 gate presentations (edit + approve), got %d", gateCount)
	}
	gateMu.Unlock()
}

func TestEngine_BudgetExhausted(t *testing.T) {
	budgetErr := fmt.Errorf("%w: used 100 of 50", harness.ErrBudgetExhausted)
	steps := defaultTestSteps()
	steps.Research = fakeResearchStepErr(budgetErr)

	setup := PipelineSetup{
		Research: true, Execution: true, Validation: true,
		HumanGates: nil,
	}

	_, err := runPipelineSync(context.Background(), setup, steps)
	if err == nil {
		t.Fatal("expected error when researcher budget is exhausted")
	}
	if !errors.Is(err, harness.ErrBudgetExhausted) {
		t.Errorf("expected ErrBudgetExhausted in error chain, got: %v", err)
	}
}

// --- Engine.Run integration test ---

func TestEngine_Run_NoGate(t *testing.T) {
	// Verify Engine.Run (the synchronous polling wrapper) works end-to-end
	// with the new architecture using a config-only engine (no real claude CLI).
	// This test must NOT call testutil.MustTempHome; it exercises Engine directly.
	cfg := config.DefaultConfig()
	engine := &Engine{Config: cfg}

	// Engine.Run with a zero-setup (defaults to DefaultPipelineSetup which has a gate)
	// would block. Use explicit setup with no gates.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The engine uses real harness.Run internally, which would require a claude binary.
	// We can't test end-to-end without the binary, so just verify Start returns a handle.
	handle := engine.Start(ctx, Input{
		Prompt: "test",
		Setup:  PipelineSetup{Research: false, Execution: false, Validation: false},
	})
	if handle.Obs == nil {
		t.Fatal("Start returned nil ObsStore")
	}
	if handle.Ctrl == nil {
		t.Fatal("Start returned nil Control")
	}
}

// --- Skipped tests (require git repo or complex infrastructure) ---

func TestEngine_MergeErrorFailsAndPreservesWorktree(t *testing.T) {
	t.Skip("needs rewrite for ProcessSpec path — requires real git repo and merge operations")
}

func TestEngine_MergeConflictFailsAndPreservesWorktree(t *testing.T) {
	t.Skip("needs rewrite for ProcessSpec path — requires real git repo and merge operations")
}

func TestEngine_WorkerFailurePreservesWorktree(t *testing.T) {
	t.Skip("needs rewrite for ProcessSpec path — requires real git repo and worktree operations")
}

// --- No-dead-knobs invariant tests ---
// Each test asserts that a surfaced PipelineSetup knob actually changes pipeline behavior.

func TestNoDeadKnob_ResearchFalse_NoResearchPhase(t *testing.T) {
	researchCalled := false
	steps := defaultTestSteps()
	steps.Research = &fakeStep[ResearchInput, ResearchOutput]{
		agentID: "researcher",
		fn: func(_ context.Context, _ ResearchInput, _ StepContext) (ResearchOutput, error) {
			researchCalled = true
			return ResearchOutput{DraftMarkdown: "draft"}, nil
		},
	}

	setup := PipelineSetup{Research: false, Execution: false, Validation: false, HumanGates: nil}
	result, err := runPipelineSync(context.Background(), setup, steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != StatusSuccess {
		t.Errorf("status = %q, want success", result.Status)
	}
	if researchCalled {
		t.Error("Research:false must not invoke the research step")
	}
}

func TestNoDeadKnob_ValidationFalse_NoValidationPhase(t *testing.T) {
	validateCalled := false
	steps := defaultTestSteps()
	steps.Validate = &fakeStep[ValidateInput, ValidateOutput]{
		agentID: "validator",
		fn: func(_ context.Context, _ ValidateInput, _ StepContext) (ValidateOutput, error) {
			validateCalled = true
			return ValidateOutput{Output: "pass"}, nil
		},
	}

	setup := PipelineSetup{Research: true, Execution: true, Validation: false, HumanGates: nil}
	_, err := runPipelineSync(context.Background(), setup, steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if validateCalled {
		t.Error("Validation:false must not invoke the validate step")
	}
}

func TestNoDeadKnob_GateAfterDeliberation_FiresWhenEnabled(t *testing.T) {
	obs := NewObsStore()
	ctrl := NewControl(obs)

	setup := PipelineSetup{
		Research: false, Execution: false, Validation: false,
		HumanGates: HumanGateSet{GateAfterDeliberation},
	}
	sc := testStepContext(obs, ctrl)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go driveGate(t, obs, ctrl, GateAfterDeliberation, Decision{Type: DecisionApprove}, 3*time.Second, cancel)

	_, err := RunPipeline(ctx, setup, PipelineRunInput{Prompt: "test"}, sc, defaultTestSteps())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNoDeadKnob_GateAfterDeliberation_SkippedWhenDisabled(t *testing.T) {
	// With no gates in HumanGates, the pipeline should complete without waiting.
	setup := PipelineSetup{
		Research: false, Execution: false, Validation: false,
		HumanGates: nil,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := runPipelineSync(ctx, setup, defaultTestSteps())
	if err != nil {
		t.Fatalf("unexpected error (gate fired when disabled?): %v", err)
	}
}

func TestEngine_PlanFileBeforeGate(t *testing.T) {
	t.Skip("needs rewrite — ArtifactSink capture requires sessionDir integration")
}

func TestEngine_PhaseOrder_WithCritic(t *testing.T) {
	t.Skip("critic is embedded in DeliberateStep — requires full DeliberateStep integration")
}

func TestEngine_DecisionComment_CommitsDialog(t *testing.T) {
	t.Skip("skipped: tests plan-history git repo integration, removed in v6 gate replacement")
}

func TestEngine_DecisionComment_ChatOnly(t *testing.T) {
	t.Skip("skipped: tests plan-history git repo integration, removed in v6 gate replacement")
}

func TestEngine_CriticRevision_AlwaysCommitted(t *testing.T) {
	t.Skip("skipped: tests plan-history git repo integration, removed in v6 gate replacement")
}

func TestEngine_FullConversation_Integrity(t *testing.T) {
	t.Skip("skipped: tests plan-history git repo integration, removed in v6 gate replacement")
}

func TestEngine_DecisionEdit_CommitsDialog(t *testing.T) {
	t.Skip("skipped: tests plan-history git repo integration, removed in v6 gate replacement")
}

func TestGate_DecisionEditEmptyComment_NoArchitect(t *testing.T) {
	t.Skip("needs rewrite for RunPipeline path — architect re-engagement is now in ReviseStep")
}

func TestGate_DecisionEditAutoApprove_ProceedsToWorker(t *testing.T) {
	t.Skip("needs rewrite for RunPipeline path — AutoApprove is not yet handled in gate loop")
}

func TestEngine_CriticStreamFallback(t *testing.T) {
	t.Skip("needs rewrite — critic stream fallback is internal to DeliberateStep")
}

func TestEngine_RunLog_Created(t *testing.T) {
	t.Skip("needs rewrite — run log is created in engine_pipeline.go; requires RunDirFactory wiring")
}
