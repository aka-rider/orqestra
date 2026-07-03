package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/mcp"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// TestExitCodeForStatus_Table covers WP16 QA gate (e): every RunStatus maps
// to exactly the exit code documented in root CLAUDE.md §4's table, and an
// unrecognized/empty status never falls back to a success code (§0).
func TestExitCodeForStatus_Table(t *testing.T) {
	tests := []struct {
		name   string
		status orchestrator.RunStatus
		want   int
	}{
		{"success", orchestrator.StatusSuccess, exitOK},
		{"failed", orchestrator.StatusFailed, exitDomainFailure},
		{"cancelled", orchestrator.StatusCancelled, exitUserCancelled},
		{"empty/unknown never maps to success", orchestrator.RunStatus(""), exitDomainFailure},
		{"unrecognized never maps to success", orchestrator.RunStatus("bogus"), exitDomainFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCodeForStatus(tt.status); got != tt.want {
				t.Errorf("exitCodeForStatus(%q) = %d, want %d", tt.status, got, tt.want)
			}
		})
	}
}

// closedEvents returns a channel pre-loaded with evs and already closed,
// matching RunHandle.Events' documented contract: EventRunFinished is always
// last, and the channel closes immediately after it.
func closedEvents(evs ...orchestrator.RunEvent) chan orchestrator.RunEvent {
	ch := make(chan orchestrator.RunEvent, len(evs))
	for _, e := range evs {
		ch <- e
	}
	close(ch)
	return ch
}

// TestConsumeHeadlessEvents_Success drains a normal success run and checks
// the printed lifecycle lines and exit code.
func TestConsumeHeadlessEvents_Success(t *testing.T) {
	events := closedEvents(
		orchestrator.EventPhaseStarted{Phase: orchestrator.PhasePlanning},
		orchestrator.EventAgentStarted{AgentID: "architect", Meta: orchestrator.AgentMeta{ModelRef: "qwen"}},
		orchestrator.EventAgentDone{AgentID: "architect", Usage: harness.TokenUsage{Input: 100, Output: 50}},
		orchestrator.EventReportHarvested{AgentID: "architect", Provenance: orchestrator.ReportProvenance{Tier: 3, Source: orchestrator.SourceFinalMessage}},
		orchestrator.EventRunFinished{Result: orchestrator.Result{Status: orchestrator.StatusSuccess, FinalPlan: "# Plan\n"}},
	)
	intents := make(chan orchestrator.Intent, 4)
	handle := orchestrator.RunHandle{Events: events, Intents: intents}

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	var stdout, stderr bytes.Buffer
	code := consumeHeadlessEvents(ctx, cancel, handle, &stdout, &stderr, false)

	if code != exitOK {
		t.Errorf("exit code = %d, want exitOK (%d); stderr=%s", code, exitOK, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "[phase] planning") {
		t.Errorf("stdout missing phase line: %q", out)
	}
	if !strings.Contains(out, "[agent] architect started") {
		t.Errorf("stdout missing agent-started line: %q", out)
	}
	if !strings.Contains(out, "[agent] architect done (tokens in=100 out=50)") {
		t.Errorf("stdout missing agent-done line with token counts: %q", out)
	}
	if !strings.Contains(out, "[report] architect harvested (tier=3 source=final_message)") {
		t.Errorf("stdout missing report-harvested line: %q", out)
	}
	if !strings.Contains(out, "[done] status=success") {
		t.Errorf("stdout missing final status line: %q", out)
	}
}

// TestConsumeHeadlessEvents_GateOpenedFailsFast proves WP16's headless
// contract: a human gate opening (only possible without --auto-approve)
// prints the explicit error, cancels ctx, and overrides the exit code to
// exitInvalidInput regardless of what RunFinished's Result.Status says —
// never a hang, never a silent success.
func TestConsumeHeadlessEvents_GateOpenedFailsFast(t *testing.T) {
	events := closedEvents(
		orchestrator.EventPhaseStarted{Phase: orchestrator.PhasePlanning},
		orchestrator.EventGateOpened{GateID: 1, Request: orchestrator.GateRequest{Position: orchestrator.GateAfterDeliberation}},
		orchestrator.EventGateClosed{GateID: 1},
		// Even a Result claiming success must never win over gateBlocked.
		orchestrator.EventRunFinished{Result: orchestrator.Result{Status: orchestrator.StatusSuccess}},
	)
	intents := make(chan orchestrator.Intent, 4)
	handle := orchestrator.RunHandle{Events: events, Intents: intents}

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	var stdout, stderr bytes.Buffer
	code := consumeHeadlessEvents(ctx, cancel, handle, &stdout, &stderr, false)

	if code != exitInvalidInput {
		t.Errorf("exit code = %d, want exitInvalidInput (%d)", code, exitInvalidInput)
	}
	if !strings.Contains(stderr.String(), "human gate requires --auto-approve or the TUI") {
		t.Errorf("stderr missing explicit gate error: %q", stderr.String())
	}
	select {
	case <-ctx.Done():
		if !errors.Is(context.Cause(ctx), orchestrator.ErrUserCancelled) {
			t.Errorf("ctx cancel cause = %v, want ErrUserCancelled", context.Cause(ctx))
		}
	default:
		t.Error("ctx was never cancelled on gate-open fail-fast")
	}
}

// TestConsumeHeadlessEvents_QuestionAutoSkipped proves an AskUserQuestion in
// headless mode is answered immediately with Skipped:true (no human is
// attached) rather than left for a caller who will never respond.
func TestConsumeHeadlessEvents_QuestionAutoSkipped(t *testing.T) {
	events := closedEvents(
		orchestrator.EventQuestionAsked{ToolCall: mcp.ToolCall{ID: "q-1", Question: "proceed?"}},
		orchestrator.EventRunFinished{Result: orchestrator.Result{Status: orchestrator.StatusSuccess}},
	)
	intents := make(chan orchestrator.Intent, 4)
	handle := orchestrator.RunHandle{Events: events, Intents: intents}

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	var stdout, stderr bytes.Buffer
	code := consumeHeadlessEvents(ctx, cancel, handle, &stdout, &stderr, false)

	if code != exitOK {
		t.Errorf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "[question] q-1 skipped") {
		t.Errorf("stdout missing question-skipped line: %q", stdout.String())
	}

	select {
	case in := <-intents:
		ans, ok := in.(orchestrator.QuestionAnswerIntent)
		if !ok {
			t.Fatalf("intent type = %T, want QuestionAnswerIntent", in)
		}
		if ans.QuestionID != "q-1" || !ans.Answer.Skipped || ans.Answer.ID != "q-1" {
			t.Errorf("answer = %+v, want QuestionID=q-1 Answer{ID:q-1,Skipped:true}", ans)
		}
	default:
		t.Fatal("no QuestionAnswerIntent sent on handle.Intents")
	}
}
