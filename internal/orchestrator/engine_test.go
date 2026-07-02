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

// fakeValidateStep returns a step that emits a fixed validation string.
func fakeValidateStep(output string) Step[ValidateInput, ValidateOutput] {
	return &fakeStep[ValidateInput, ValidateOutput]{
		agentID: "worker",
		fn: func(ctx context.Context, in ValidateInput, sc StepContext) (ValidateOutput, error) {
			return ValidateOutput{Output: output}, nil
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
		Setup:  PipelineSetup{Execution: false, Validation: false},
	})
	if handle.Obs == nil {
		t.Fatal("Start returned nil ObsStore")
	}
	if handle.Ctrl == nil {
		t.Fatal("Start returned nil Control")
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


