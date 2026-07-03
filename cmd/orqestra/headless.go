package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/mcp"
	"github.com/xiii/orqestra/internal/orchestrator"
	"github.com/xiii/orqestra/internal/sandbox"
)

// runHeadlessCommand drives one pipeline run to completion without the TUI
// (WP16/J18): it builds the engine exactly as the TUI path does (buildEngine
// is untouched — a sibling refactor owns that function), starts the
// QuestionBridge the same way tui.Run does, and consumes the run's events
// itself since no human is present to drive a Bubble Tea program.
//
// It owns its own root context (cancel-cause) because for this one
// invocation it — not tui.Run — is the top-level driver: SIGINT/SIGTERM and
// an unattended human gate are both distinguishable stop reasons attributed
// via context.Cause (root CLAUDE.md §1.3), the same mechanism
// engine_pipeline.go already uses to classify ErrUserCancelled.
func runHeadlessCommand(cfg *config.Config, sandboxProfiles []sandbox.Snapshot, repoPath, prompt string, autoApprove, planOnly, verboseStream bool, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	// SIGINT/SIGTERM cancel the run context with a distinguishable cause so a
	// headless run is always interruptible — the harness's own process-group
	// kill on cancel (sandbox.go) is unchanged; this only triggers it. The
	// goroutine exits via ctx.Done(), so it never outlives this call.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		defer signal.Stop(sigCh)
		select {
		case <-sigCh:
			cancel(orchestrator.ErrUserCancelled)
		case <-ctx.Done():
		}
	}()

	engine := buildEngine(cfg, sandboxProfiles, repoPath)

	// Mirror tui.Run's QuestionBridge wiring (mcp.StartBridgeAsync — bridge
	// Run started exactly once). Scoped to ctx rather than a separate
	// context: a headless invocation drives exactly one run, so "this run's
	// lifetime" and "the whole session" are the same lifetime here.
	mcp.StartBridgeAsync(ctx, engine.QuestionBridge)

	setup := orchestrator.DefaultPipelineSetup()
	if planOnly {
		setup.Execution = false
		setup.Validation = false
	}
	if autoApprove {
		// No gate ever fires — the simplest honest semantics for a caller
		// that explicitly asked not to be prompted.
		setup.HumanGates = nil
	}

	handle := engine.Start(ctx, orchestrator.Input{
		Prompt:     prompt,
		Setup:      setup,
		SetupValid: true,
	})

	return consumeHeadlessEvents(ctx, cancel, handle, stdout, stderr, verboseStream)
}

// consumeHeadlessEvents drains handle.Events to completion, printing one
// plain-text lifecycle line per event to stdout — no TUI dependency. It
// answers every AskUserQuestion with Skipped:true (no human is attached in
// headless mode) and fails fast the instant a human gate opens: a headless
// run must never silently hang waiting for a decision no one can make.
func consumeHeadlessEvents(ctx context.Context, cancel context.CancelCauseFunc, handle orchestrator.RunHandle, stdout, stderr io.Writer, verboseStream bool) int {
	var gateBlocked bool
	var final orchestrator.Result
	var finalErr error

	for ev := range handle.Events {
		switch e := ev.(type) {
		case orchestrator.EventPhaseStarted:
			fmt.Fprintf(stdout, "[phase] %s\n", e.Phase)

		case orchestrator.EventAgentStarted:
			fmt.Fprintf(stdout, "[agent] %s started (model=%s)\n", e.AgentID, agentModelLabel(e.Meta))

		case orchestrator.EventAgentDone:
			fmt.Fprintf(stdout, "[agent] %s done (tokens in=%d out=%d)\n", e.AgentID, e.Usage.Input, e.Usage.Output)

		case orchestrator.EventAgentFailed:
			fmt.Fprintf(stdout, "[agent] %s failed: %v\n", e.AgentID, e.Err)

		case orchestrator.EventReportHarvested:
			fmt.Fprintf(stdout, "[report] %s harvested (tier=%d source=%s)\n", e.AgentID, e.Provenance.Tier, e.Provenance.Source)

		case orchestrator.EventDelta:
			if verboseStream {
				fmt.Fprint(stdout, e.Text)
			}

		case orchestrator.EventGateOpened:
			// A gate can only open here when the caller did not pass
			// --auto-approve (auto-approve sets HumanGates=nil, so this case
			// is never reached in that mode) — every EventGateOpened seen in
			// headless mode IS the "no human attached" case. Fail fast:
			// print the explicit error, cancel the run so the blocked gate
			// unblocks via ctx.Done() (gate.go), and let the run's own
			// EventRunFinished follow — gateBlocked overrides whatever
			// RunStatus that carries so the exit code always reflects "a
			// gate needed a human that wasn't there," not the pipeline's own
			// (accurate but secondary) failed/cancelled classification.
			fmt.Fprintf(stderr, "Error: human gate requires --auto-approve or the TUI (gate: %s)\n", e.Request.Position)
			gateBlocked = true
			cancel(orchestrator.ErrUserCancelled)

		case orchestrator.EventQuestionAsked:
			fmt.Fprintf(stdout, "[question] %s skipped (no human attached in headless mode)\n", e.ToolCall.ID)
			select {
			case handle.Intents <- orchestrator.QuestionAnswerIntent{
				QuestionID: e.ToolCall.ID,
				Answer:     mcp.Answer{ID: e.ToolCall.ID, Skipped: true},
			}:
			case <-ctx.Done():
			}

		case orchestrator.EventRunFinished:
			final = e.Result
			finalErr = e.Err
		}
	}

	if len(final.ConflictFiles) > 0 {
		fmt.Fprintf(stdout, "[conflict] unresolved files: %s\n", strings.Join(final.ConflictFiles, ", "))
	}
	if final.ValidationVerdict != "" {
		fmt.Fprintf(stdout, "[validation] verdict=%s\n", final.ValidationVerdict)
	}
	fmt.Fprintf(stdout, "[done] status=%s\n", statusOrUnknown(final.Status))
	if finalErr != nil {
		fmt.Fprintf(stderr, "Error: %v\n", finalErr)
	}

	if gateBlocked {
		return exitInvalidInput
	}
	return exitCodeForStatus(final.Status)
}

// agentModelLabel picks the most specific available label for an agent's
// model — ModelDisplay when the config supplied one, else the raw ModelRef.
func agentModelLabel(meta orchestrator.AgentMeta) string {
	if meta.ModelDisplay != "" {
		return meta.ModelDisplay
	}
	return meta.ModelRef
}

// statusOrUnknown renders a RunStatus for the final status line, never a
// bare empty string (which would read as truncated output rather than an
// explicit, if unexpected, state).
func statusOrUnknown(status orchestrator.RunStatus) string {
	if status == "" {
		return "unknown"
	}
	return string(status)
}

// exitCodeForStatus maps a terminal RunStatus to a process exit code (root
// CLAUDE.md §4's exit-code table).
func exitCodeForStatus(status orchestrator.RunStatus) int {
	switch status {
	case orchestrator.StatusSuccess:
		return exitOK
	case orchestrator.StatusCancelled:
		return exitUserCancelled
	case orchestrator.StatusFailed:
		return exitDomainFailure
	default:
		// RunFinished always carries one of the three statuses above
		// (engine_pipeline.go); an empty/unrecognized value means something
		// upstream never set it — never invent a success code for that (§0).
		return exitDomainFailure
	}
}
