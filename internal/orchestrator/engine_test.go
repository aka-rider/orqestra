package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

// fakeValidateStep returns a step that emits a fixed validation string,
// parsed the same way ValidateStep.Run parses real worker output (J33/WP8) —
// so tests exercising RunPipeline's verdict threading see a real Parsed.Verdict,
// not an always-zero one.
func fakeValidateStep(output string) Step[ValidateInput, ValidateOutput] {
	return &fakeStep[ValidateInput, ValidateOutput]{
		agentID: "worker",
		fn: func(ctx context.Context, in ValidateInput, sc StepContext) (ValidateOutput, error) {
			return ValidateOutput{Output: output, Parsed: agent.ParseValidationOutput(output)}, nil
		},
	}
}

// noopStepContext returns a StepContext with nil Executor — engine routing tests
// don't call sc.Exec.Run(); nil makes any accidental call panic visibly.
func noopStepContext(obs Observer, ctrl Control) StepContext {
	return StepContext{
		Exec:      nil,
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
	sc := noopStepContext(obs, ctrl)
	return RunPipeline(ctx, setup, PipelineRunInput{Prompt: "test prompt", RunID: "test-run"}, sc, steps)
}

// --- Active tests ---

func TestEngine_PlanApprovalGate(t *testing.T) {
	// INV-O1-FLOW: gate blocks pipeline; DecisionApprove resumes it → StatusSuccess.
	obs := NewObsStore()
	ctrl := NewControl(obs)
	sc := noopStepContext(obs, ctrl)

	setup := PipelineSetup{
		Execution: true, Validation: true,
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
	// INV-O1-FLOW: DecisionCancel at gate → StatusCancelled.
	obs := NewObsStore()
	ctrl := NewControl(obs)
	sc := noopStepContext(obs, ctrl)

	setup := PipelineSetup{
		Execution: true, Validation: true,
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
	// INV-O1-FLOW: no HumanGates → pipeline completes without blocking.
	setup := PipelineSetup{
		Execution: true, Validation: true,
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

	sc := noopStepContext(recordingObs, ctrl)

	setup := PipelineSetup{
		Execution: true, Validation: true,
		HumanGates: nil,
	}

	_, err := RunPipeline(context.Background(), setup, PipelineRunInput{Prompt: "Add feature X", RunID: "run-1"}, sc, defaultTestSteps())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	got := append([]Phase(nil), phases...)
	mu.Unlock()

	expected := []Phase{PhasePlanning, PhaseExecuting, PhaseSelfValidating}
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

	sc := noopStepContext(recordingObs, ctrl)

	setup := PipelineSetup{
		Execution: false, Validation: false,
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
		Execution: true, Validation: true,
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
	// J33/WP8: the parsed verdict must reach Result — not be silently discarded.
	// Validation stays advisory (status above is still StatusSuccess); only the
	// verdict itself must be truthfully carried through.
	if result.ValidationVerdict != agent.VerdictFail {
		t.Errorf("ValidationVerdict = %q, want %q (J33: parsed verdict discarded)", result.ValidationVerdict, agent.VerdictFail)
	}
}

func TestEngine_ValidationSuccessDetection(t *testing.T) {
	steps := defaultTestSteps()
	steps.Validate = fakeValidateStep(agent.MarkerPass + " tests pass\n" + agent.MarkerPass + " build ok")

	setup := PipelineSetup{
		Execution: true, Validation: true,
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
	if result.ValidationVerdict != agent.VerdictPass {
		t.Errorf("ValidationVerdict = %q, want %q (J33: parsed verdict discarded)", result.ValidationVerdict, agent.VerdictPass)
	}
}

func TestEngine_DecisionEdit(t *testing.T) {
	// INV-O1-FLOW: DecisionEdit at gate → re-run with edited content → DecisionApprove → StatusSuccess.
	obs := NewObsStore()
	ctrl := NewControl(obs)
	sc := noopStepContext(obs, ctrl)

	setup := PipelineSetup{
		Execution: true, Validation: true,
		HumanGates: HumanGateSet{GateAfterDeliberation},
	}
	steps := defaultTestSteps()

	editedContent := "# Plan\n\n## Goal\nEdited.\n\n## Work Packages\n\n### 1. Do it\n\n**Steps:**\n1. Edit foo.go\n\n**Done when:**\n- Tests pass"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// First gate: send Edit; second gate: send Approve.
	//
	// Edge-triggered via Rev, not via re-observing HasGate: NotifyCh is a
	// coalescing wake signal and Snapshot is a point-in-time poll, so a stale
	// wakeup can observe the SAME still-open gate twice before Gate() has
	// consumed the decision and closed it. Without dedup that double-counts
	// gate 1 as gate 2, submits the Approve decision while gate 1's Edit
	// decision is still the one buffered (Approve is silently dropped, cap-1
	// channel full), and this goroutine returns having never satisfied the
	// real second gate — which then blocks until ctx times out. GateOpened
	// always bumps Rev, so a distinct opening always carries an unhandled Rev.
	gateCount := 0
	var gateMu sync.Mutex
	go func() {
		var lastHandledRev uint64
		handled := false
		for {
			snap := obs.Snapshot()
			if snap.HasGate && snap.Gate.Position == GateAfterDeliberation && (!handled || snap.Rev != lastHandledRev) {
				handled = true
				lastHandledRev = snap.Rev
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
	steps.Deliberate = &fakeStep[DeliberateInput, PlanOutput]{
		agentID: "architect",
		fn: func(_ context.Context, _ DeliberateInput, _ StepContext) (PlanOutput, error) {
			return PlanOutput{}, budgetErr
		},
	}

	setup := PipelineSetup{
		Execution: true, Validation: true,
		HumanGates: nil,
	}

	_, err := runPipelineSync(context.Background(), setup, steps)
	if err == nil {
		t.Fatal("expected error when deliberation budget is exhausted")
	}
	if !errors.Is(err, harness.ErrBudgetExhausted) {
		t.Errorf("expected ErrBudgetExhausted in error chain, got: %v", err)
	}
}

// --- Engine.Start integration test ---

func TestEngine_Start_NoGate(t *testing.T) {
	// Verify Engine.Start works end-to-end with the new architecture using a
	// config-only engine (no real claude CLI).
	// This test must NOT call testutil.MustTempHome; it exercises Engine directly.
	cfg := config.DefaultConfig()
	engine := &Engine{Config: cfg}

	// An unset Setup (SetupValid=false) defaults to DefaultPipelineSetup, which
	// has a gate and would block. Use an explicit, valid, no-gates setup instead.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The engine uses real harness.Run internally, which would require a claude binary.
	// We can't test end-to-end without the binary, so just verify Start returns a handle.
	handle := engine.Start(ctx, Input{
		Prompt:     "test",
		Setup:      PipelineSetup{Execution: false, Validation: false, DeliberationRounds: 1},
		SetupValid: true,
	})
	if handle.Obs == nil {
		t.Fatal("Start returned nil ObsStore")
	}
	if handle.Ctrl == nil {
		t.Fatal("Start returned nil Control")
	}
}

// TestEngineStart_SetupValidInvalidSetup_FailsRun is the J24 end-to-end proof:
// a caller that explicitly asks for an all-zero-fields "everything off"
// PipelineSetup (SetupValid=true) with an invalid DeliberationRounds must have
// the run FAIL with the validation error — never silently substitute
// DefaultPipelineSetup (which would enable Execution and could let a worker
// modify the repo the caller only asked to plan for). This never reaches
// harness.Run: PipelineSetup.Validate() fails before any agent is invoked, so
// the test needs no claude binary and completes fast.
func TestEngineStart_SetupValidInvalidSetup_FailsRun(t *testing.T) {
	cfg := config.DefaultConfig()
	engine := &Engine{Config: cfg}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// All-zero fields: exactly the shape that used to trip the old
	// zero-value-detection heuristic into silently substituting defaults,
	// regardless of the caller's explicit intent.
	handle := engine.Start(ctx, Input{
		Prompt:     "test",
		Setup:      PipelineSetup{},
		SetupValid: true,
	})

	for {
		snap := handle.Obs.Snapshot()
		if snap.Terminal.Done {
			if snap.Terminal.Result.Status != StatusFailed {
				t.Fatalf("expected StatusFailed for an invalid explicit setup, got %s", snap.Terminal.Result.Status)
			}
			if snap.Terminal.Err == nil {
				t.Fatal("expected a non-nil error for an invalid explicit setup, got nil (setup was silently defaulted?)")
			}
			if !strings.Contains(snap.Terminal.Err.Error(), "invalid pipeline setup") {
				t.Errorf("expected 'invalid pipeline setup' in the terminal error, got: %v", snap.Terminal.Err)
			}
			return
		}
		select {
		case <-handle.Obs.NotifyCh():
		case <-ctx.Done():
			t.Fatal("timed out waiting for the run to fail on invalid explicit setup — was it silently defaulted and left waiting on a gate?")
		}
	}
}

// TestEngineStart_DoesNotSwapGlobalLogger is the J4 regression proof: a run
// with a real session directory (so the per-run log-file logger path is
// exercised) must NEVER install its logger as the process-global default via
// slog.SetDefault. Before the fix, engine_pipeline.go called
// slog.SetDefault(logger) on every run with a session directory and reset the
// global to an io.Discard-backed logger in a deferred cleanup afterward —
// racing concurrent/overlapping runs and silently discarding ALL process
// logging once the first run completed. slog.Default() identity must be
// unchanged before and after the run.
func TestEngineStart_DoesNotSwapGlobalLogger(t *testing.T) {
	before := slog.Default()

	cfg := config.DefaultConfig()
	engine := &Engine{
		Config: cfg,
		RunDirFactory: func(slug string) (agent.SessionDir, error) {
			return agent.SessionDir{Path: t.TempDir()}, nil
		},
		Specs: ProcessSpecs{
			// A binary that cannot exist makes harness.Run fail immediately
			// (no such file) without invoking any real claude CLI, while still
			// exercising the full startNew goroutine — including opening
			// session.Path/run.log, the exact code path J4's slog.SetDefault
			// calls used to wrap.
			Architect: harness.ProcessSpec{Binary: "/nonexistent-orqestra-test-binary-j4"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	handle := engine.Start(ctx, Input{
		Prompt:     "test",
		Setup:      PipelineSetup{Execution: false, Validation: false, DeliberationRounds: 1},
		SetupValid: true,
	})

	for {
		snap := handle.Obs.Snapshot()
		if snap.Terminal.Done {
			break
		}
		select {
		case <-handle.Obs.NotifyCh():
		case <-ctx.Done():
			t.Fatal("timed out waiting for the run to complete")
		}
	}

	after := slog.Default()
	if before != after {
		t.Error("slog.Default() identity changed across the run — the per-run logger (or its io.Discard reset) leaked into the process-global default")
	}
}

// --- No-dead-knobs invariant tests ---
// Each test asserts that a surfaced PipelineSetup knob actually changes pipeline behavior.

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

	setup := PipelineSetup{Execution: true, Validation: false, HumanGates: nil}
	_, err := runPipelineSync(context.Background(), setup, steps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if validateCalled {
		t.Error("Validation:false must not invoke the validate step")
	}
}

func TestNoDeadKnob_GateAfterDeliberation_FiresWhenEnabled(t *testing.T) {
	// INV-O1-FLOW: GateAfterDeliberation in HumanGates → gate fires and blocks.
	obs := NewObsStore()
	ctrl := NewControl(obs)

	setup := PipelineSetup{
		Execution: false, Validation: false,
		HumanGates: HumanGateSet{GateAfterDeliberation},
	}
	sc := noopStepContext(obs, ctrl)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go driveGate(t, obs, ctrl, GateAfterDeliberation, Decision{Type: DecisionApprove}, 3*time.Second, cancel)

	_, err := RunPipeline(ctx, setup, PipelineRunInput{Prompt: "test"}, sc, defaultTestSteps())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
