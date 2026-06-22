package orchestrator

import (
	"context"
	"fmt"
	"io"
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
func (e *Engine) startNew(ctx context.Context, input Input) RunHandle {
	obs := NewObsStore()
	ctrl := NewControl(obs)

	go func() {
		// Setup session directory.
		var session agent.SessionDir
		if e.RunDirFactory != nil {
			var err error
			session, err = e.RunDirFactory("run")
			if err != nil {
				obs.Finished(Result{Status: StatusFailed}, fmt.Errorf("create run directory: %w", err))
				return
			}
		}

		// Per-run logger.
		logger := slog.Default()
		if session.Path != "" {
			logPath := filepath.Join(session.Path, "run.log")
			logFile, logErr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if logErr != nil {
				slog.Warn("could not create run log", "err", logErr)
			} else {
				logger = slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug}))
				slog.SetDefault(logger)
				defer func() {
					slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
					logFile.Close()
				}()
			}
		}
		logger.Info("run started (pipeline)", "prompt_len", len(input.Prompt))

		// Write prompt artifact.
		if session.Path != "" {
			artifacts := NewArtifactSink(session)
			artifacts.WriteBestEffort("prompt.md", []byte(input.Prompt))
		}

		// Question bridge.
		if e.QuestionBridge != nil {
			go func() {
				if err := e.QuestionBridge.Run(ctx); err != nil {
					logger.Warn("question bridge", "err", err)
				}
			}()
			go func() {
				for {
					select {
					case q, ok := <-e.QuestionBridge.Questions():
						if !ok {
							return
						}
						obs.UserQuestion(q)
					case <-ctx.Done():
						return
					}
				}
			}()
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
			Artifacts: NewArtifactSink(session),
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

		if err := setup.Validate(); err != nil {
			obs.Finished(Result{Status: StatusFailed}, fmt.Errorf("invalid pipeline setup: %w", err))
			return
		}

		// Wire deliberation rounds into the step. HasCritic=false is handled by the early
		// return inside DeliberateStep.Run, so Rounds is irrelevant in that case.
		deliberate, ok := steps.Deliberate.(*DeliberateStep)
		if !ok {
			obs.Finished(Result{Status: StatusFailed}, fmt.Errorf("internal: Deliberate step is not *DeliberateStep"))
			return
		}
		deliberate.Rounds = setup.DeliberationRounds

		result, err := RunPipeline(ctx, setup, PipelineRunInput{
			Prompt: input.Prompt,
			RunID:  filepath.Base(session.Path),
		}, sc, steps)

		result.RunDir = session.Path
		obs.Finished(result, err)
		logger.Info("run_complete", "status", string(result.Status))
	}()

	return RunHandle{Obs: obs, Ctrl: ctrl}
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
