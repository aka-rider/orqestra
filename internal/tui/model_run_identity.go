package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// model_run_identity.go holds the WP17/F1,A3 "run identity on the
// event/intent chain" plumbing: runEventMsg/waitForEvent (which carry a
// RunID so Model.Update can reject a stale run's late delivery) and
// closeIntentsDone (which releases sendIntent Cmds once a run can no longer
// receive them). Split out of model.go to keep it under the 500-line
// guideline (root CLAUDE.md §1.7) — Model.activeRunID/intentsDone
// themselves stay on Model in model.go, since state resides with its owner.

// runEventMsg wraps one orchestrator.RunEvent for the Bubble Tea message
// loop (WP10 — replaces the pre-WP10 notify-wakeup + snapshot-diffing
// message pair). RunEvent crosses the orchestrator→TUI package boundary
// already carrying its own producer-defined type identity; this thin
// wrapper is the "orchestration events flow down through typed messages"
// case internal/tui/CLAUDE.md §2.4 calls out.
//
// runID (WP17/F1,A3) identifies which run this event came from — captured
// at waitForEvent's call site, NOT read from Model at delivery time, since
// by the time Update handles this message the model's activeRunID may
// already have moved on (the user cancelled and started a new run). Update
// compares runID against m.activeRunID and drops (no ApplyEvent, no re-arm)
// anything that doesn't match: a stale chain dies on its first delivery.
type runEventMsg struct {
	runID orchestrator.RunID
	ev    orchestrator.RunEvent
}

// waitForEvent returns a tea.Cmd that receives exactly one RunEvent from
// events and wraps it as a runEventMsg carrying runID. The caller re-arms by
// calling this again after handling each runEventMsg — see Update's
// runEventMsg case, which stops re-arming once it sees EventRunFinished
// (events closes immediately after that event, so the chain would stop on
// its own anyway, but stopping explicitly is clearer than relying on
// channel-close/nil-msg semantics) or once the message's runID no longer
// matches the model's active run (WP17/F1: a stale chain must die on its
// first delivery, not just stop rendering — never re-arm it).
func waitForEvent(runID orchestrator.RunID, events <-chan orchestrator.RunEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-events
		if !ok {
			return nil
		}
		return runEventMsg{runID: runID, ev: ev}
	}
}

// closeIntentsDone releases every sendIntent Cmd still waiting to send on
// the current run's intents channel, then clears intentsDone so a later
// call (there should never be one, but this stays defensive/idempotent) is
// a safe no-op instead of a double-close panic. Called from every place
// that ends a run's ability to receive intents: natural completion
// (Update's runEventMsg/EventRunFinished case) and user abandonment
// (ConfirmNewRunIntent/NavigateToPromptIntent, model_intents.go).
func (m *Model) closeIntentsDone() {
	if m.intentsDone != nil {
		close(m.intentsDone)
		m.intentsDone = nil
	}
}
