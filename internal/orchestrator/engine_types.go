package orchestrator

import (
	"errors"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/mcp"
	"github.com/xiii/orqestra/internal/rundir"
)

// ErrUserCancelled is the cancellation cause the TUI attributes to the root run
// context when the user cancels a running pipeline (e.g. Ctrl+C). Distinguishes
// an intentional stop from an internal failure in run.log and Result.Status.
var ErrUserCancelled = errors.New("run cancelled by user")

// Input is the user's request to the orchestrator.
// SetupValid distinguishes "caller did not provide a setup" (use defaults)
// from "caller explicitly chose this PipelineSetup" (use it as-is, even when
// every field is a zero value — e.g. plan-only, no gates). Without this
// marker a genuine all-zero-fields request is indistinguishable from an
// unset Setup and gets silently replaced by DefaultPipelineSetup, which
// enables Execution — a caller that asked only to plan could have a worker
// run and modify the repo (J24).
type Input struct {
	Prompt     string
	Setup      PipelineSetup // used as-is when SetupValid; ignored otherwise
	SetupValid bool
}

// RunStatus classifies the final outcome.
type RunStatus string

const (
	StatusSuccess   RunStatus = "success"
	StatusFailed    RunStatus = "failed"
	StatusCancelled RunStatus = "cancelled"
)

// Result is the final output of an orchestrator run.
type Result struct {
	Status           RunStatus
	FinalPlan        string
	WorkerValidation string
	// ValidationVerdict is the parsed verdict from worker self-validation
	// (agent.ParseValidationOutput / ValidateOutput.Parsed.Verdict — J33). Empty
	// when validation did not run (Validation disabled or Validate step nil);
	// otherwise one of agent.VerdictPass/VerdictWarn/VerdictFail. Validation
	// stays advisory: a FAIL verdict here does NOT fail the pipeline unless
	// pipeline.block_merge_on_validation_fail is enabled (see run_pipeline.go).
	ValidationVerdict agent.Verdict
	RunDir            string
	// ConflictFiles is populated when Integrate gives up on a merge conflict
	// (see IntegrateOutput.ConflictFiles); empty otherwise.
	ConflictFiles []string
}

// RunDirFactory creates a session directory for artifact persistence.
type RunDirFactory func(slug string) (rundir.Dir, error)

// ProcessSpecs holds per-role ProcessSpec values for the RunPipeline path.
type ProcessSpecs struct {
	Architect harness.ProcessSpec
	Critic    harness.ProcessSpec
	Worker    harness.ProcessSpec
	// WorktreeSpecFn returns a spec for the worker scoped to the given worktree path.
	// If nil, Worker spec is used with direct repo access.
	WorktreeSpecFn func(wtPath string) harness.ProcessSpec
	// Integrator is used for the commit-message generation mode (no tools, RO sandbox).
	Integrator harness.ProcessSpec
	// IntegratorConflictSpecFn returns a spec for the integrator's conflict-resolution
	// mode scoped to the given worktree path (Read/Edit tools, worktree-writable sandbox).
	// A returned error means the spec could not be built; IntegrateStep.handleConflict
	// treats it as give-up-and-preserve, never executes a zero ProcessSpec (J19).
	IntegratorConflictSpecFn func(wtPath string) (harness.ProcessSpec, error)
}

// Engine is the hardcoded Go orchestrator that runs the full pipeline.
type Engine struct {
	Config         *config.Config
	RepoPath       string // canonical project root (from .orqestra or .git detection)
	Specs          ProcessSpecs
	RunDirFactory  RunDirFactory
	QuestionBridge *mcp.QuestionBridge
}

// RunHandle is the whole TUI (or headless driver) ⇄ engine surface for one
// run (WP10/RC1 — "one pipe out, one pipe in"; replaces the pre-WP10
// Obs/Ctrl pair). Events is the ordered, one-way observation stream; Intents
// is the one-way channel back — every gate decision and question answer is
// correlated by GateID/QuestionID, so a stale or mismatched intent is
// dropped rather than misapplied (WP4a/WP5 invariants, preserved by
// construction — see gate.go and engine_pipeline.go's intents consumer).
type RunHandle struct {
	// Events is the WP9/WP10 ordered event bus for this run (RunEvent — see
	// event.go/emitter.go). Always a real, non-nil channel (startNew always
	// attaches an emitter); a run completes without blocking even when
	// nobody ever reads from it — see emitter.go's "No-consumer policy".
	// Closed exactly once, immediately after the EventRunFinished event.
	Events <-chan RunEvent

	// Intents is the send-only channel a caller uses to deliver gate
	// decisions and question answers back to the running pipeline.
	// Buffered (intentBufSize) so a caller's send is very unlikely ever to
	// block in practice — engine_pipeline.go's intents consumer drains it
	// continuously — but a caller that could ever send from a context that
	// must not block (e.g. a Bubble Tea Update) should still wrap the send
	// in an async command, since "never" is not "cannot" (plan WP10 step 7).
	Intents chan<- Intent

	// forwarderDone is closed once this run's question-forwarder goroutine has
	// been joined (WP4b/J5,J41) — always before Finished is called, so an
	// observer reacting to the run's terminal state (e.g. starting the next
	// run) never races this run's forwarder teardown, guaranteeing at most
	// one consumer of the shared QuestionBridge.Questions() channel at a
	// time. Unexported: white-box test instrumentation only, invisible
	// outside this package (the TUI never depends on it).
	forwarderDone chan struct{}

	// runDone is closed as the LAST statement of the run's pipeline
	// goroutine (after Finished has been called) — independent of whether
	// Events is ever drained. Unexported: white-box test instrumentation
	// only, used to prove the "no consumer never blocks the pipeline"
	// invariant (WP9 QA gate (c)) without relying on the pre-WP10
	// snapshot store, which no longer exists.
	runDone chan struct{}
}
