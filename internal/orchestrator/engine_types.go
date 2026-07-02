package orchestrator

import (
	"errors"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/mcp"
)

// ErrUserCancelled is the cancellation cause the TUI attributes to the root run
// context when the user cancels a running pipeline (e.g. Ctrl+C). Distinguishes
// an intentional stop from an internal failure in run.log and Result.Status.
var ErrUserCancelled = errors.New("run cancelled by user")

// RestartInput carries context for restarting a failed or incomplete run.
type RestartInput struct {
	RunPath string       // session directory of the original run
	Phase   RestartPhase // which phase to restart from
}

// Input is the user's request to the orchestrator.
type Input struct {
	Prompt      string
	RestartFrom RestartInput
	Setup       PipelineSetup // optional pipeline configuration (set by TUI setup panel)
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
	RunDir           string
	// ConflictFiles is populated when Integrate gives up on a merge conflict
	// (see IntegrateOutput.ConflictFiles); empty otherwise.
	ConflictFiles []string
}

// RunDirFactory creates a session directory for artifact persistence.
type RunDirFactory func(slug string) (agent.SessionDir, error)

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
	IntegratorConflictSpecFn func(wtPath string) harness.ProcessSpec
}

// Engine is the hardcoded Go orchestrator that runs the full pipeline.
type Engine struct {
	Config         *config.Config
	RepoPath       string // canonical project root (from .orqestra or .git detection)
	Specs          ProcessSpecs
	RunDirFactory  RunDirFactory
	QuestionBridge *mcp.QuestionBridge
}

// RunHandle provides the TUI with observation and control handles for an active run.
// The pipeline writes non-blocking updates to Obs; the TUI polls Obs.Snapshot() on
// each notify wakeup or tick. Control carries gate + live Post semantics.
type RunHandle struct {
	Obs  *ObsStore
	Ctrl Control

	// forwarderDone is closed once this run's question-forwarder goroutine has
	// been joined (WP4b/J5,J41) — always before Obs.Finished is called, so an
	// observer reacting to Obs's terminal state (e.g. starting the next run)
	// never races this run's forwarder teardown, guaranteeing at most one
	// consumer of the shared QuestionBridge.Questions() channel at a time.
	// Unexported: white-box test instrumentation only, invisible outside this
	// package (the TUI never depends on it).
	forwarderDone chan struct{}
}
