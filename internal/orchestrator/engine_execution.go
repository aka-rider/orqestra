package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/xiii/orqestra/internal/agent"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/worktree"
)

// executionResult holds the output of the execution phase.
type executionResult struct {
	WorkOutput       string
	WorkerSessionID  string
	WorkerUsage      harness.TokenUsage
	ValidationOutput string
	ValParsed        agent.ValidationOutput
	Status           RunStatus
}

// runExecution executes the worker agent and writes its artifacts.
// Returns the execution result on success, or returns an error event and false on failure.
func (e *Engine) runExecution(
	ctx context.Context,
	session agent.SessionDir,
	emit func(Event),
	logger *slog.Logger,
	agentStart map[string]time.Time,
	stream *streamCapture,
	streamOut chan<- harness.Event,
	finalPlanMarkdown string,
	workMeta AgentMeta,
) (*executionResult, bool) {
	// Determine which branch to merge back into after the run.
	targetBranch, branchErr := worktree.CurrentBranch(ctx, e.RepoPath)
	if branchErr != nil {
		slog.Warn("cannot determine current branch — worktree isolation disabled", "err", branchErr)
	}

	// Create an isolated git worktree for the worker when possible.
	var wt worktree.Worktree
	var wtErr error
	workerRunner := e.Runners.Worker

	workerStart := time.Now()
	runID := ""
	if session.Path != "" && targetBranch != "" && e.WorktreeRunnerFactory != nil {
		runID = fmt.Sprintf("%d", workerStart.UnixMilli())
		wt, wtErr = worktree.Create(ctx, e.RepoPath, session.Path, runID)
		if wtErr != nil {
			slog.Warn("worktree creation failed — falling back to writable repo", "err", wtErr)
			wt = worktree.Worktree{} // zero value = no worktree
		} else {
			workerRunner = e.WorktreeRunnerFactory(wt.Path)
		}
	}

	// --- Worker Execution ---
	emit(Event{Type: EventPhaseChange, Phase: PhaseExecuting})
	logger.Info("phase", "phase", string(PhaseExecuting))
	logAgentEvent(logger, "agent_started", "worker", 1, harness.TokenUsage{}, nil, agentStart)
	emit(Event{Type: EventAgentStarted, AgentID: "worker", Meta: resolveAgentMeta(e.Config, e.Config.Worker.Model)})
	stream.SetAgent("worker")

	execPrompt := agent.BuildExecutionPromptFromPlan(finalPlanMarkdown)
	workResult, execErr := runRunnerStreaming(ctx, workerRunner, execPrompt, "", stream, streamOut)

	if execErr != nil {
		e.failWorker(session, workerRunner, logger, agentStart, workMeta, workerStart, execErr, workResult, stream, streamOut, emit)
		return nil, false
	}

	workerLogCopy, cpErr := copySessionLog(session, "", workResult.SessionID, "worker_session.jsonl")
	if cpErr != nil {
		logger.Warn("copy session log", "agent", "worker", "err", cpErr)
	}
	writeArtifact(session, "worker_output.txt", workResult.Output)
	writeArtifactJSON(session, "worker_meta.json", agent.StepMeta{
		AgentID: "worker", ModelRef: e.Config.Worker.Model, StartTime: workerStart, EndTime: time.Now(),
		ModelDisplay: workMeta.ModelDisplay, Provider: workMeta.Provider, ContextWindow: workMeta.ContextWindow,
		ClaudeSessionID: workResult.SessionID, Status: "done",
		InputTokens: workResult.Usage.Input, OutputTokens: workResult.Usage.Output,
		ClaudeProjectPath:    claudeProjectPath(session),
		ClaudeSessionLogPath: workerLogCopy,
	})
	logClaudeSession(logger, "worker", 1, workResult.SessionID, workerLogCopy, session)
	logAgentEvent(logger, "agent_done", "worker", 1, workResult.Usage, nil, agentStart)
	emit(Event{Type: EventAgentDone, AgentID: "worker", WorkOutput: workResult.Output,
		InputTokens: workResult.Usage.Input, OutputTokens: workResult.Usage.Output})

	// lastSessionID tracks the most recent session continuation — used for
	// commit message generation after validation completes.
	lastSessionID := workResult.SessionID

	// Run validation (may be nil if validation is disabled).
	valOutput, valParsed := e.runValidation(ctx, session, emit, logger, agentStart, stream, streamOut,
		workerRunner, workMeta, lastSessionID)

	// Derive run status from parsed validation verdict.
	status := StatusSuccess
	if valParsed.Verdict == agent.VerdictFail {
		status = StatusFailed
	}

	// --- Post-run worktree commit + merge ---
	mergeOutcomeFailed := e.runWorktreeMerge(ctx, wt, targetBranch, runID, lastSessionID, workerRunner, logger, emit)
	if mergeOutcomeFailed {
		status = StatusFailed
	}

	return &executionResult{
		WorkOutput:       workResult.Output,
		WorkerSessionID:  workResult.SessionID,
		WorkerUsage:      workResult.Usage,
		ValidationOutput: valOutput,
		ValParsed:        valParsed,
		Status:           status,
	}, true
}

func (e *Engine) failWorker(
	session agent.SessionDir,
	workerRunner harness.Runner,
	logger *slog.Logger,
	agentStart map[string]time.Time,
	workMeta AgentMeta,
	workerStart time.Time,
	err error,
	workResult harness.RunResult,
	stream *streamCapture,
	streamOut chan<- harness.Event,
	emit func(Event),
) {
	writeArtifactJSON(session, "worker_meta.json", agent.StepMeta{
		AgentID: "worker", ModelRef: e.Config.Worker.Model, StartTime: workerStart, EndTime: time.Now(),
		ModelDisplay: workMeta.ModelDisplay, Provider: workMeta.Provider, ContextWindow: workMeta.ContextWindow,
		Status: "failed", Error: err.Error(),
		ClaudeProjectPath: claudeProjectPath(session),
	})
	if workResult.SessionID == "" {
		logClaudeSessionPre(logger, "worker", 1, "", session)
	} else {
		logClaudeSessionPre(logger, "worker", 1, workResult.SessionID, session)
	}
	logAgentEvent(logger, "agent_failed", "worker", 1, harness.TokenUsage{}, err, agentStart)
	emit(Event{Type: EventAgentFailed, AgentID: "worker", Err: err})
	emit(Event{Type: EventError, Err: err})
}

// runValidation executes worker self-validation via session continuation.
func (e *Engine) runValidation(
	ctx context.Context,
	session agent.SessionDir,
	emit func(Event),
	logger *slog.Logger,
	agentStart map[string]time.Time,
	stream *streamCapture,
	streamOut chan<- harness.Event,
	workerRunner harness.Runner,
	workMeta AgentMeta,
	lastSessionID string,
) (string, agent.ValidationOutput) {
	valStart := time.Now()
	var validationOutput string
	valParsed := agent.ValidationOutput{}

	// Only run validation if enabled.
	// (The caller checks setup.Validation before calling this function.)

	// --- Worker Self-Validation via Session Continuation ---
	emit(Event{Type: EventPhaseChange, Phase: PhaseSelfValidating})
	logger.Info("phase", "phase", string(PhaseSelfValidating))
	logAgentEvent(logger, "agent_started", "validator", 1, harness.TokenUsage{}, nil, agentStart)
	emit(Event{Type: EventAgentStarted, AgentID: "validator", Meta: resolveAgentMeta(e.Config, e.Config.Worker.Model)})
	stream.SetAgent("validator")

	retryBudget := e.Config.Retry.WorkerValidationRetries
	if retryBudget < 1 {
		retryBudget = 1
	}

	validationPrompt := agent.WorkerValidationPrompt(retryBudget)

	if lastSessionID != "" {
		valResult, valErr := runRunnerContinue(ctx, workerRunner, lastSessionID, validationPrompt, stream, streamOut)
		if valErr != nil {
			slog.Warn("worker self-validation failed", "err", valErr)
			valLogCopy, cpErr := copySessionLog(session, "", lastSessionID, "validator_session.jsonl")
			if cpErr != nil {
				logger.Warn("copy session log", "agent", "validator", "err", cpErr)
			}
			writeArtifactJSON(session, "validator_meta.json", agent.StepMeta{
				AgentID: "validator", ModelRef: e.Config.Worker.Model, StartTime: valStart, EndTime: time.Now(),
				ModelDisplay: workMeta.ModelDisplay, Provider: workMeta.Provider, ContextWindow: workMeta.ContextWindow,
				ClaudeSessionID: lastSessionID, Status: "failed", Error: valErr.Error(),
				ClaudeProjectPath:    claudeProjectPath(session),
				ClaudeSessionLogPath: valLogCopy,
			})
			logClaudeSession(logger, "validator", 1, lastSessionID, valLogCopy, session)
			logAgentEvent(logger, "agent_failed", "validator", 1, harness.TokenUsage{}, valErr, agentStart)
			emit(Event{Type: EventAgentFailed, AgentID: "validator", Err: valErr})
			// Non-fatal: proceed with whatever output we have
		} else {
			validationOutput = valResult.Output
			valLogCopy, cpErr := copySessionLog(session, "", valResult.SessionID, "validator_session.jsonl")
			if cpErr != nil {
				logger.Warn("copy session log", "agent", "validator", "err", cpErr)
			}
			writeArtifactJSON(session, "validator_meta.json", agent.StepMeta{
				AgentID: "validator", ModelRef: e.Config.Worker.Model, StartTime: valStart, EndTime: time.Now(),
				ModelDisplay: workMeta.ModelDisplay, Provider: workMeta.Provider, ContextWindow: workMeta.ContextWindow,
				ClaudeSessionID: valResult.SessionID, Status: "done",
				InputTokens: valResult.Usage.Input, OutputTokens: valResult.Usage.Output,
				ClaudeProjectPath:    claudeProjectPath(session),
				ClaudeSessionLogPath: valLogCopy,
			})
			logClaudeSession(logger, "validator", 1, valResult.SessionID, valLogCopy, session)
			logAgentEvent(logger, "agent_done", "validator", 1, valResult.Usage, nil, agentStart)
			emit(Event{Type: EventAgentDone, AgentID: "validator",
				InputTokens: valResult.Usage.Input, OutputTokens: valResult.Usage.Output})
		}
	} else {
		logger.Warn("validator session missing, running disconnected")
		// Fallback: run validation as a new session (less effective but still useful)
		valResult, valErr := runRunnerStreaming(ctx, workerRunner, validationPrompt, "", stream, streamOut)
		if valErr != nil {
			slog.Warn("disconnected validation failed", "err", valErr)
			writeArtifactJSON(session, "validator_meta.json", agent.StepMeta{
				AgentID: "validator", ModelRef: e.Config.Worker.Model, StartTime: valStart, EndTime: time.Now(),
				ModelDisplay: workMeta.ModelDisplay, Provider: workMeta.Provider, ContextWindow: workMeta.ContextWindow,
				Status: "failed", Error: valErr.Error(),
				ClaudeProjectPath: claudeProjectPath(session),
			})
			logClaudeSessionPre(logger, "validator", 1, "", session)
			logAgentEvent(logger, "agent_failed", "validator", 1, harness.TokenUsage{}, valErr, agentStart)
			emit(Event{Type: EventAgentFailed, AgentID: "validator", Err: valErr})
		} else {
			validationOutput = valResult.Output
			discValLogCopy, cpErr := copySessionLog(session, "", valResult.SessionID, "validator_session.jsonl")
			if cpErr != nil {
				logger.Warn("copy session log", "agent", "validator", "err", cpErr)
			}
			writeArtifactJSON(session, "validator_meta.json", agent.StepMeta{
				AgentID: "validator", ModelRef: e.Config.Worker.Model, StartTime: valStart, EndTime: time.Now(),
				ModelDisplay: workMeta.ModelDisplay, Provider: workMeta.Provider, ContextWindow: workMeta.ContextWindow,
				ClaudeSessionID: valResult.SessionID, Status: "done",
				InputTokens: valResult.Usage.Input, OutputTokens: valResult.Usage.Output,
				ClaudeProjectPath:    claudeProjectPath(session),
				ClaudeSessionLogPath: discValLogCopy,
			})
			logClaudeSession(logger, "validator", 1, valResult.SessionID, discValLogCopy, session)
			logAgentEvent(logger, "agent_done", "validator", 1, valResult.Usage, nil, agentStart)
			emit(Event{Type: EventAgentDone, AgentID: "validator",
				InputTokens: valResult.Usage.Input, OutputTokens: valResult.Usage.Output})
		}
	}

	// Parse validation output into structured result.
	valParsed = agent.ParseValidationOutput(validationOutput)
	writeArtifact(session, "worker_validation.txt", validationOutput)

	return validationOutput, valParsed
}

// runWorktreeCommitMsg generates a semantic commit message and performs worktree commit + merge.
func (e *Engine) runWorktreeMerge(
	ctx context.Context,
	wt worktree.Worktree,
	targetBranch, runID, lastSessionID string,
	workerRunner harness.Runner,
	logger *slog.Logger,
	emit func(Event),
) bool {
	mergeOutcomeFailed := false

	// --- Commit message generation ---
	// back to a generic message. Only attempted when there is a worktree to
	// commit and a session to continue.
	semanticMsg := ""
	if wt.Path != "" && lastSessionID != "" {
		workerRunner.SetEvents(nil)
		workerRunner.Post(agent.CommitMessagePrompt())
		var msgResult harness.RunResult
		for ev := range workerRunner.Receive() {
			if ev.Kind == harness.EventError {
				msgErr := fmt.Errorf("commit message generation: %s", ev.Text)
				slog.Warn("commit message generation failed — using fallback", "err", msgErr)
				msgResult = harness.RunResult{Output: ev.Text}
				break
			}
			if ev.Kind == harness.EventUsage {
				msgResult.Usage = harness.TokenUsage{Input: ev.Input, Output: ev.Output}
			}
			if ev.Kind == harness.EventChunk && ev.Text != "" {
				msgResult.Output += ev.Text
			}
		}
		if msgResult.Output != "" {
			parsed, parseErr := agent.ParseCommitMessage(msgResult.Output)
			if parseErr != nil {
				slog.Warn("commit message parse failed — using fallback", "err", parseErr)
			} else {
				semanticMsg = parsed
			}
		}
	}

	// buildCommitMsg returns the full commit message: the semantic summary (or a
	// generic fallback) followed by a compact run-ID trailer on its own paragraph.
	buildCommitMsg := func(fallbackPrefix string) string {
		msg := semanticMsg
		if msg == "" {
			msg = fallbackPrefix + ": Orqestra automated run"
		}
		return msg + "\n\nrun: " + runID + " by Orqestra"
	}

	// --- Post-run worktree commit + merge ---
	if wt.Path != "" {
		commitMsg := buildCommitMsg("feat")
		committed, commitErr := wt.CommitAll(ctx, commitMsg)
		if commitErr != nil {
			slog.Warn("worktree commit failed — skipping merge", "err", commitErr)
		} else if !committed {
			slog.Info("worktree: nothing to commit — merge skipped")
		}

		if committed && commitErr == nil {
			mergeResult, mergeErr := wt.MergeInto(ctx, targetBranch, buildCommitMsg("merge"))
			if mergeErr != nil {
				mergeOutcomeFailed = true
				slog.Warn("worktree merge failed", "err", mergeErr)
				emit(Event{
					Type:              EventMergeError,
					MergeError:        mergeErr.Error(),
					MergeBranch:       wt.Branch,
					MergeWorktreePath: wt.Path,
				})
			} else if !mergeResult.Merged {
				mergeOutcomeFailed = true
				// Conflicts — gate the user unless AutoApprove is set.
				emit(Event{
					Type: EventMergeConflict,
					MergeConflict: MergeConflictInfo{
						WorktreeBranch: wt.Branch,
						WorktreePath:   wt.Path,
						TargetBranch:   targetBranch,
						ConflictFiles:  mergeResult.ConflictFiles,
					},
					MergeWorktreePath: wt.Path,
				})
				logger.Warn("merge_conflict", "worktree_branch", wt.Branch, "target_branch", targetBranch, "files", len(mergeResult.ConflictFiles))
				// Note: decision handling removed here — the caller handles it.
			} else {
				// Merge succeeded — clean up worktree and branch
				if rmErr := wt.Remove(context.Background(), true); rmErr != nil {
					slog.Warn("worktree cleanup failed", "err", rmErr)
				}
			}
		}
	}

	return mergeOutcomeFailed
}
