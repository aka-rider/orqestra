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
	"github.com/xiii/orqestra/internal/rundir"
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

// --- Active tests ---

func TestEngine_PlanApprovalGate(t *testing.T) {
	// INV-O1-FLOW: gate blocks pipeline; DecisionApprove resumes it → StatusSuccess.
	sc, events, decisions := newGateTestContext()

	setup := PipelineSetup{
		Execution: true, Validation: true,
		HumanGates: HumanGateSet{GateAfterDeliberation},
	}
	steps := defaultTestSteps()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go driveGate(t, events, decisions, GateAfterDeliberation, Decision{Type: DecisionApprove}, 5*time.Second, cancel)

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
	sc, events, decisions := newGateTestContext()

	setup := PipelineSetup{
		Execution: true, Validation: true,
		HumanGates: HumanGateSet{GateAfterDeliberation},
	}
	steps := defaultTestSteps()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go driveGate(t, events, decisions, GateAfterDeliberation, Decision{Type: DecisionCancel}, 5*time.Second, cancel)

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
	// defaultTestSteps() are fakes that never call sc.Obs themselves — the
	// only events this run emits are RunPipeline's own PhaseChanged calls, so
	// draining exactly 3 events off the bus after RunPipeline returns
	// deterministically captures the full phase sequence.
	sc, events, _ := newGateTestContext()

	setup := PipelineSetup{
		Execution: true, Validation: true,
		HumanGates: nil,
	}

	_, err := RunPipeline(context.Background(), setup, PipelineRunInput{Prompt: "Add feature X", RunID: "run-1"}, sc, defaultTestSteps())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := collectPhases(t, events, 3, 5*time.Second)

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

func TestEngine_NoExecute(t *testing.T) {
	sc, events, _ := newGateTestContext()

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

	// Execution disabled → only the PhasePlanning event, never PhaseExecuting.
	got := collectPhases(t, events, 1, 5*time.Second)
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
	sc, events, decisions := newGateTestContext()

	setup := PipelineSetup{
		Execution: true, Validation: true,
		HumanGates: HumanGateSet{GateAfterDeliberation},
	}
	steps := defaultTestSteps()

	editedContent := "# Plan\n\n## Goal\nEdited.\n\n## Work Packages\n\n### 1. Do it\n\n**Steps:**\n1. Edit foo.go\n\n**Done when:**\n- Tests pass"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// First gate: send Edit; second gate: send Approve. Unlike the pre-WP10
	// Snapshot/NotifyCh polling loop, EventGateOpened is edge-triggered — every
	// delivery is a genuinely NEW gate opening, so no Rev-based dedup is
	// needed to avoid double-counting a still-open gate.
	gateCount := 0
	var gateMu sync.Mutex
	go func() {
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					return
				}
				g, ok := ev.(EventGateOpened)
				if !ok || g.Request.Position != GateAfterDeliberation {
					continue
				}
				gateMu.Lock()
				gateCount++
				n := gateCount
				gateMu.Unlock()
				if n == 1 {
					decisions <- GateDecisionIntent{GateID: g.GateID, Decision: Decision{Type: DecisionEdit, EditedContent: editedContent}}
				} else {
					decisions <- GateDecisionIntent{GateID: g.GateID, Decision: Decision{Type: DecisionApprove}}
					return
				}
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
	if handle.Events == nil {
		t.Fatal("Start returned a nil Events channel")
	}
	if handle.Intents == nil {
		t.Fatal("Start returned a nil Intents channel")
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

	result, err := waitRunFinished(t, handle.Events, 5*time.Second)
	if result.Status != StatusFailed {
		t.Fatalf("expected StatusFailed for an invalid explicit setup, got %s", result.Status)
	}
	if err == nil {
		t.Fatal("expected a non-nil error for an invalid explicit setup, got nil (setup was silently defaulted?)")
	}
	if !strings.Contains(err.Error(), "invalid pipeline setup") {
		t.Errorf("expected 'invalid pipeline setup' in the terminal error, got: %v", err)
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
		RunDirFactory: func(slug string) (rundir.Dir, error) {
			return rundir.Dir{Path: t.TempDir()}, nil
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

	waitRunFinished(t, handle.Events, 10*time.Second)

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
	sc, events, decisions := newGateTestContext()

	setup := PipelineSetup{
		Execution: false, Validation: false,
		HumanGates: HumanGateSet{GateAfterDeliberation},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go driveGate(t, events, decisions, GateAfterDeliberation, Decision{Type: DecisionApprove}, 3*time.Second, cancel)

	_, err := RunPipeline(ctx, setup, PipelineRunInput{Prompt: "test"}, sc, defaultTestSteps())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
