package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/rundir"
)

// startNew is the RunPipeline-based pipeline path. It wires the WP9 emitter
// directly behind an eventObserver (no snapshot-store/gate-control
// intermediary, WP10/RC1-RC2) and returns a RunHandle whose Events/Intents
// pair is the whole caller-facing surface.
//
// QuestionBridge lifecycle (WP4b/J5,J41): e.QuestionBridge.Run is started
// exactly ONCE, for the whole engine/TUI-session lifetime, by whoever hands
// the Engine to the TUI (see tui.Run) — never here. Each run only owns a
// question-FORWARDER goroutine that relays e.QuestionBridge.Questions() as
// EventQuestionAsked onto THIS run's emitter. That forwarder runs on its own
// run-scoped context (derived from ctx, not ctx itself) so it is torn down
// deterministically when this run's pipeline work concludes, independent of
// whether ctx is ever cancelled — a natural completion never cancels ctx
// (J41), so without this the forwarder would leak and stay a live consumer
// of the shared Questions() channel forever. The forwarder (and the intents
// consumer below) are joined BEFORE obs.Finished signals completion (not
// merely deferred to goroutine exit), so any observer reacting to the run's
// terminal state — the TUI, or a test starting the next run — can never
// race this run's forwarder teardown: at most one consumer of Questions()
// exists at any time, even across back-to-back runs.
//
// Intents routing (WP10): a single per-run "intents consumer" goroutine
// drains RunHandle.Intents and dispatches by concrete type — a
// GateDecisionIntent is forwarded (non-blocking, cap-1, stale ones simply
// get overwritten/dropped) to gateDecisions, the channel the gate mechanism
// itself reads with its own drain-before-open/GateID-match loop (gate.go);
// a QuestionAnswerIntent is routed directly to
// e.QuestionBridge.SendAnswer, which independently validates the ID
// (WP5/J17,J25). This keeps GateID correlation and question-ID correlation
// as two independent, non-interfering concerns sharing one inbound channel.
func (e *Engine) startNew(ctx context.Context, input Input) RunHandle {
	em := newEmitter(eventBusBufSize)
	obs := newEventObserver(em)

	intentsIn := make(chan Intent, intentBufSize)
	gateDecisions := make(chan GateDecisionIntent, 1)
	gateFn := newGateFunc(em, gateDecisions)

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
					em.Emit(EventQuestionAsked{ToolCall: q})
				case <-runCtx.Done():
					return
				}
			}
		}()
	} else {
		close(forwarderDone)
	}

	intentsDone := make(chan struct{})
	go func() {
		defer close(intentsDone)
		for {
			select {
			case in, ok := <-intentsIn:
				if !ok {
					return
				}
				switch it := in.(type) {
				case GateDecisionIntent:
					select {
					case gateDecisions <- it:
					default: // fire-and-forget: superseded by a later decision, or dropped as stale — the gate's own drain-before-open handles staleness (WP4a)
					}
				case QuestionAnswerIntent:
					if e.QuestionBridge != nil {
						e.QuestionBridge.SendAnswer(it.Answer)
					}
				}
			case <-runCtx.Done():
				return
			}
		}
	}()

	runDone := make(chan struct{})

	go func() {
		defer close(runDone)
		// finish joins the question-forwarder and intents consumer BEFORE
		// signaling terminal state — see the lifecycle comment above. Every
		// return path in this goroutine MUST go through finish, not
		// obs.Finished directly.
		finish := func(res Result, err error) {
			runCancel()
			<-forwarderDone
			<-intentsDone
			obs.Finished(res, err)
		}

		// Setup session directory.
		var session rundir.Dir
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
			Gate:      gateFn,
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

	return RunHandle{
		Events:        em.Events(),
		Intents:       intentsIn,
		forwarderDone: forwarderDone,
		runDone:       runDone,
	}
}

// eventBusBufSize sizes only the WP9 event bus's OUTPUT channel (a
// convenience for a fast, attentive consumer) — it never bounds the
// emitter's internal lifecycle-event queue (emitter.go).
const eventBusBufSize = 64

// intentBufSize sizes RunHandle.Intents — generously buffered so a caller's
// send is very unlikely ever to block (the intents consumer above drains it
// continuously); plan-simplify-architecture.md WP10 step 7 suggests cap 4 as
// the floor for "never blocks the gate-decision path in practice".
const intentBufSize = 8

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
