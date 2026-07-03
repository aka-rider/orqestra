package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/harness"
)

// startNew is the RunPipeline-based pipeline path.
// It creates ObsStore + Control and uses ObsStore directly as the Observer —
// no bridging channels, no backpressure. The TUI polls Obs.Snapshot() on each
// notify wakeup or tick.
//
// QuestionBridge lifecycle (WP4b/J5,J41): e.QuestionBridge.Run is started
// exactly ONCE, for the whole engine/TUI-session lifetime, by whoever hands
// the Engine to the TUI (see tui.Run) — never here. Each run only owns a
// question-FORWARDER goroutine that relays e.QuestionBridge.Questions() into
// THIS run's ObsStore. That forwarder runs on its own run-scoped context
// (derived from ctx, not ctx itself) so it is torn down deterministically
// when this run's pipeline work concludes, independent of whether ctx is
// ever cancelled — a natural completion never cancels ctx (J41), so without
// this the forwarder would leak and stay a live consumer of the shared
// Questions() channel forever. The forwarder is joined BEFORE obs.Finished
// signals completion (not merely deferred to goroutine exit), so any
// observer reacting to Terminal.Done — the TUI, or a test starting the next
// run — can never race this run's forwarder teardown: at most one consumer
// of Questions() exists at any time, even across back-to-back runs.
func (e *Engine) startNew(ctx context.Context, input Input) RunHandle {
	obs := NewObsStore()
	ctrl := NewControl(obs)

	runCtx, runCancel := context.WithCancel(ctx)
	forwarderDone := make(chan struct{})
	if e.QuestionBridge != nil {
		go func() {
			defer close(forwarderDone)
			for {
				select {
				case q, ok := <-e.QuestionBridge.Questions():
					if !ok {
						return
					}
					obs.UserQuestion(q)
				case <-runCtx.Done():
					return
				}
			}
		}()
	} else {
		close(forwarderDone)
	}

	go func() {
		// finish joins the question-forwarder BEFORE signaling terminal state —
		// see the lifecycle comment above. Every return path in this goroutine
		// MUST go through finish, not obs.Finished directly.
		finish := func(res Result, err error) {
			runCancel()
			<-forwarderDone
			obs.Finished(res, err)
		}

		// Setup session directory.
		var session agent.SessionDir
		if e.RunDirFactory != nil {
			var err error
			session, err = e.RunDirFactory("run")
			if err != nil {
				finish(Result{Status: StatusFailed}, fmt.Errorf("create run directory: %w", err))
				return
			}
		}

		// Per-run logger. Flows ONLY through StepContext.Log — NEVER installed as
		// the process-global default (J4): mutating the global default here would
		// race concurrent/overlapping runs and silently discard all process
		// logging after the first run's defer reset it. Any code that needs to
		// log during a run must receive this logger explicitly.
		logger := slog.Default()
		if session.Path != "" {
			logPath := filepath.Join(session.Path, "run.log")
			logFile, logErr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if logErr != nil {
				slog.Warn("could not create run log", "err", logErr)
			} else {
				logger = slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug}))
				defer logFile.Close()
			}
		}
		logger.Info("run started (pipeline)", "prompt_len", len(input.Prompt))

		// Write prompt artifact.
		if session.Path != "" {
			artifacts := NewArtifactSink(session, logger)
			artifacts.WriteBestEffort("prompt.md", []byte(input.Prompt))
		}

		// Build supervisor for process-group and worktree cleanup.
		sup := &Supervisor{}
		defer sup.Shutdown(logger)

		// Build AgentSupervisor: single owner of timeout, budget, nudge policies, and report stop.
		guard := NewBudgetGuard(NewRunUsage(e.Config.Pipeline.TokenBudget))
		var reports ReportSignaler
		if e.QuestionBridge != nil {
			reports = e.QuestionBridge
		}
		exec := NewAgentSupervisor(harness.RunFunc(harness.Run), reports, guard)

		// Build step context.
		sc := StepContext{
			Exec:      exec,
			Obs:       obs,
			Artifacts: NewArtifactSink(session, logger),
			Control:   ctrl,
			Sessions:  session,
			Log:       logger,
			RepoPath:  e.RepoPath,
		}
		if e.QuestionBridge != nil {
			sc.Reports = e.QuestionBridge
		}

		// Build pipeline steps.
		steps := e.buildPipelineSteps(sup)
		setup := resolveSetup(input)
		// BlockMergeOnValidationFail is a global safety config (like TokenBudget
		// above), not a per-run TUI setup-panel knob — always take it from Config,
		// overriding whatever the caller's Input.Setup carried (J33/WP8).
		setup.BlockMergeOnValidationFail = e.Config.Pipeline.BlockMergeOnValidationFail

		if err := setup.Validate(); err != nil {
			finish(Result{Status: StatusFailed}, fmt.Errorf("invalid pipeline setup: %w", err))
			return
		}

		// Wire deliberation rounds into the step. HasCritic=false is handled by the early
		// return inside DeliberateStep.Run, so Rounds is irrelevant in that case.
		deliberate, ok := steps.Deliberate.(*DeliberateStep)
		if !ok {
			finish(Result{Status: StatusFailed}, fmt.Errorf("internal: Deliberate step is not *DeliberateStep"))
			return
		}
		deliberate.Rounds = setup.DeliberationRounds

		result, err := RunPipeline(ctx, setup, PipelineRunInput{
			Prompt: input.Prompt,
			RunID:  filepath.Base(session.Path),
		}, sc, steps)

		if err != nil && errors.Is(context.Cause(ctx), ErrUserCancelled) {
			result.Status = StatusCancelled
		}
		result.RunDir = session.Path
		errText := ""
		if err != nil {
			errText = err.Error()
		}
		finish(result, err)
		logger.Info("run_complete", "status", string(result.Status), "err", errText)
	}()

	return RunHandle{Obs: obs, Ctrl: ctrl, forwarderDone: forwarderDone}
}

// buildPipelineSteps constructs PipelineSteps from e.Specs and e.Config.
func (e *Engine) buildPipelineSteps(sup *Supervisor) PipelineSteps {
	archMeta := resolveAgentMeta(e.Config, e.Config.Architect.Model)
	critMeta := resolveAgentMeta(e.Config, e.Config.Critic.Model)
	workMeta := resolveAgentMeta(e.Config, e.Config.Worker.Model)

	deliberate := &DeliberateStep{
		ArchSpec:       e.Specs.Architect,
		ArchMeta:       archMeta,
		ArchAttempts:   max(e.Config.Retry.ArchitectAttempts, 1),
		CriticSpec:     e.Specs.Critic,
		CriticMeta:     critMeta,
		CriticAttempts: max(e.Config.Retry.CriticAttempts, 1),
		HasCritic:      e.Config.Critic.Model != "",
	}

	revise := &ReviseStep{
		ArchSpec: e.Specs.Architect,
		ArchMeta: archMeta,
	}

	execute := &ExecuteStep{
		Spec:           e.Specs.Worker,
		Meta:           workMeta,
		RepoPath:       e.RepoPath,
		WorktreeSpecFn: e.Specs.WorktreeSpecFn,
		Sup:            sup,
	}

	validate := &ValidateStep{
		Spec:              e.Specs.Worker,
		Meta:              workMeta,
		ValidationRetries: max(e.Config.Retry.WorkerValidationRetries, 1),
	}

	integrateMeta := resolveAgentMeta(e.Config, e.Config.Integrator.Model)

	integrate := &IntegrateStep{
		CommitMsgSpec:    e.Specs.Integrator,
		ConflictSpecFn:   e.Specs.IntegratorConflictSpecFn,
		Meta:             integrateMeta,
		Sup:              sup,
		ResolveConflicts: e.Config.Integrator.ResolveConflicts,
	}

	return PipelineSteps{
		Deliberate: deliberate,
		Revise:     revise,
		Execute:    execute,
		Validate:   validate,
		Integrate:  integrate,
	}
}
