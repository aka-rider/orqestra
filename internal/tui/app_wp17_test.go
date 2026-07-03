package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/mcp"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// app_wp17_test.go holds the WP17 cross-run lifecycle hardening QA gates
// (kept out of app_test.go, which was already over the 500-line guideline
// before this work package — root CLAUDE.md §1.7's "do not pile on").

// TestTUI_CtrlCFromRunsList_CancelsActiveBackgroundRun is the WP17 QA gate
// for the pre-existing quit-with-active-run bug (model_keys.go:53-58): a
// run started on the pipeline screen stays active in the background after
// the user navigates to the runs list. ^C from THERE must still see it as
// active and cancel it before quitting — not skip straight to tea.Quit
// (leaving the run's process group orphaned, per WP1's kill-on-cancel
// design).
//
// RED-first proof (quoted verbatim in the WP17 report): with
// `pipelineActive` gated on `m.state == StatePipeline` (the pre-fix shape),
// this test's `m.state = StateRunsList` makes pipelineActive false even
// though the pipeline is genuinely active in the background — the first ^C
// takes the "nothing to cancel" branch and returns tea.Quit directly,
// cancelCause is never called, and this test's assertion fails. Checking
// only m.pipelineScreen.active/content (the real fix, independent of the
// currently-VIEWED screen) makes it pass.
func TestTUI_CtrlCFromRunsList_CancelsActiveBackgroundRun(t *testing.T) {
	m := testModel()
	m.state = StateRunsList
	m.pipelineScreen.active = true
	m.pipelineScreen.content = ContentStreaming
	cancelled := false
	m.cancelCause = func(error) { cancelled = true }

	result, cmd := sendCtrl(m, 'c')
	model := result.(Model)

	if !cancelled {
		t.Fatal("expected cancelCause to be called on ^C from the runs list with an active background run — the process group would otherwise be orphaned on quit")
	}
	if cmd == nil {
		t.Fatal("expected a non-nil cmd (the ctrlC timeout tick) — the first ^C should start the cancel-then-quit gate, not quit immediately")
	}
	if !model.ctrlCPending {
		t.Error("expected ctrlCPending=true after the first ^C from the runs list")
	}
}

// TestTUI_SecondQuestionQueuedWhileFirstOpen is the WP17/F3 QA gate: a
// second, live EventQuestionAsked arriving while the chat already has one
// open must be queued (truthfully surfaced later), never silently dropped.
//
// RED-first proof (quoted verbatim in the WP17 report): with the pre-fix
// `onQuestionAsked` (`if !s.chat.QuestionOpen() { s.chat.OpenQuestion(...) }`
// and no queue), the second question is discarded outright — this test's
// "queued while first is open" assertion fails (pendingQuestions stays
// empty), and after resolving the first, the chat has no question left to
// show at all (never mind the second one) — a live agent's real question
// silently lost. Queuing (the real fix) makes it pass.
func TestTUI_SecondQuestionQueuedWhileFirstOpen(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.active = true
	m.pipelineScreen.content = ContentStreaming

	m = applyEvent(m, orchestrator.EventQuestionAsked{ToolCall: mcp.ToolCall{ID: "q-1", Question: "first?"}})
	if !m.pipelineScreen.chat.QuestionOpen() {
		t.Fatal("setup: expected the first question open")
	}

	// A second, live question arrives while the first is still open.
	m = applyEvent(m, orchestrator.EventQuestionAsked{ToolCall: mcp.ToolCall{ID: "q-2", Question: "second?"}})

	if len(m.pipelineScreen.pendingQuestions) != 1 {
		t.Fatalf("expected the second question queued (1 pending), got %d — a live question was dropped (F3)", len(m.pipelineScreen.pendingQuestions))
	}
	if got := m.pipelineScreen.chat.question.QuestionText(); got != "first?" {
		t.Fatalf("expected the FIRST question to remain open while queuing the second, got %q", got)
	}

	// Resolve the first (Escape → Skipped, mirroring the existing question
	// test pattern).
	ps, _ := m.pipelineScreen.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m.pipelineScreen = ps

	if !m.pipelineScreen.chat.QuestionOpen() {
		t.Fatal("expected the queued second question to surface once the first resolved — instead it was lost (F3)")
	}
	if got := m.pipelineScreen.chat.question.QuestionText(); got != "second?" {
		t.Errorf("expected the queued SECOND question to now be open, got %q", got)
	}
	if len(m.pipelineScreen.pendingQuestions) != 0 {
		t.Errorf("expected the pending queue drained after surfacing, got %d remaining", len(m.pipelineScreen.pendingQuestions))
	}
}

// TestTUI_CrossRunEventBleed_StaleRunFinishedDropped is the WP17/F1,A3 QA
// gate: reproduces the review's exact scenario — run 1 is active, the user
// presses ^N (ConfirmNewRunIntent) which cancels run 1 and starts run 2 on a
// brand-new events channel/RunID, and THEN run 1's late EventRunFinished
// (queued before the cancel took effect, delivered after) arrives. It must
// be dropped outright: no ApplyEvent, no re-arm, and run 2's screen must
// keep accepting run 2's own events afterward, in order.
//
// RED-first proof (quoted verbatim in the WP17 report): with runEventMsg
// carrying no run identity (the pre-fix shape — Update accepts every
// runEventMsg unconditionally), delivering run 1's stale EventRunFinished
// after ^N flips m.pipelineScreen.content to ContentCompletion — painting a
// false terminal state over the live run 2 — and this test fails. Gating
// Update's runEventMsg case on msg.runID == m.activeRunID (the real fix)
// makes it pass.
func TestTUI_CrossRunEventBleed_StaleRunFinishedDropped(t *testing.T) {
	m := testModel()
	m.state = StatePipeline
	m.pipelineScreen.content = ContentStreaming
	_ = m.pipelineScreen.Start("run one goal")
	m.pipelineScreen.active = true

	// --- Run 1 in flight ---
	const run1ID orchestrator.RunID = 1
	events1 := make(chan orchestrator.RunEvent, 4)
	m.events = events1
	m.activeRunID = run1ID

	events1 <- orchestrator.EventAgentStarted{AgentID: "architect"}
	cmd := waitForEvent(run1ID, events1)
	msg := cmd()
	result, cmd := m.Update(msg)
	m = result.(Model)
	if cmd == nil {
		t.Fatal("setup: expected a re-armed Cmd for run 1's own event")
	}
	// cmd here is the re-armed waitForEvent(run1ID, events1) — kept for later,
	// simulating a Cmd that bubbletea has already scheduled but not yet
	// invoked at the moment the user presses ^N below.
	staleRun1Cmd := cmd

	// --- User presses ^N: cancel run 1, start run 2 ---
	confirmResult, _ := m.processIntent(ConfirmNewRunIntent{}, nil)
	m = confirmResult.(Model)
	if m.activeRunID == run1ID {
		t.Fatal("expected activeRunID to be invalidated by ConfirmNewRunIntent before run 2 starts")
	}

	const run2ID orchestrator.RunID = 2
	events2 := make(chan orchestrator.RunEvent, 4)
	m.pipelineScreen.content = ContentStreaming
	_ = m.pipelineScreen.Start("run two goal")
	m.pipelineScreen.active = true
	m.state = StatePipeline
	m.recalculateLayout()
	m.pipelineScreen.timeline.SetRect(m.regions.timeline)
	m.events = events2
	m.activeRunID = run2ID
	run2Cmd := waitForEvent(run2ID, events2)

	// --- Run 1's late EventRunFinished arrives, delivered via the STALE Cmd
	// captured before ^N ever happened (exactly what bubbletea would do with
	// an already-in-flight Cmd) ---
	events1 <- orchestrator.EventRunFinished{Result: orchestrator.Result{Status: orchestrator.StatusCancelled}}
	staleMsg := staleRun1Cmd()
	staleResult, staleCmd := m.Update(staleMsg)
	m = staleResult.(Model)

	if m.pipelineScreen.content == ContentCompletion {
		t.Fatal("run 1's stale EventRunFinished flipped run 2's screen to completion — cross-run event bleed (F1)")
	}
	if !m.pipelineScreen.active {
		t.Fatal("run 1's stale EventRunFinished marked run 2 as no longer active — cross-run event bleed (F1)")
	}
	if staleCmd != nil {
		t.Fatal("a dropped stale event must never re-arm waitForEvent on the old (dead) run — would create a second concurrent consumer")
	}

	// --- Run 2's own events must still render, in order, on run2Cmd ---
	events2 <- orchestrator.EventAgentStarted{AgentID: "worker"}
	events2 <- orchestrator.EventDelta{AgentID: "worker", Text: "run two streaming"}
	for i := 0; i < 2 && run2Cmd != nil; i++ {
		msg2 := run2Cmd()
		var r2 tea.Model
		r2, run2Cmd = m.Update(msg2)
		m = r2.(Model)
	}

	if !strings.Contains(m.pipelineScreen.timeline.View(), "run two streaming") {
		t.Errorf("expected run 2's delta to render after the stale run-1 event was dropped, got:\n%s", m.pipelineScreen.timeline.View())
	}
	if m.pipelineScreen.content == ContentCompletion {
		t.Error("run 2 was incorrectly marked complete")
	}
}
