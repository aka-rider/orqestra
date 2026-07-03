package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/orchestrator"
)

// TestSendIntent_DropsAfterRunEnded_NeverBlocksForever is the WP17
// hardening QA gate: sendIntent's Cmd must not block its goroutine forever
// once the run it was sending to has already ended (done closed) — even
// when the intents channel is unbuffered and nobody will ever read from it
// again.
//
// RED-first proof (quoted verbatim in the WP17 report): with the pre-fix
// shape (`if ch != nil { ch <- in }`, no done parameter at all), this send
// blocks forever on the unbuffered channel — the test times out waiting for
// cmd() to return. Selecting on done (the real fix) makes it return
// promptly, dropping the intent instead.
func TestSendIntent_DropsAfterRunEnded_NeverBlocksForever(t *testing.T) {
	ch := make(chan orchestrator.Intent) // unbuffered: a completed send here proves someone read it
	done := make(chan struct{})
	close(done) // simulate: this run has already ended

	cmd := sendIntent(ch, done, orchestrator.GateDecisionIntent{GateID: 1})

	resultCh := make(chan tea.Msg, 1)
	go func() {
		resultCh <- cmd()
	}()

	select {
	case <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("sendIntent's Cmd blocked forever after its run's intentsDone was already closed (WP17 hardening note)")
	}
}

// TestSendIntent_NilChannelIsNoop covers the pre-existing defensive
// nil-guard (no pipeline running): sendIntent must not panic or block.
func TestSendIntent_NilChannelIsNoop(t *testing.T) {
	cmd := sendIntent(nil, nil, orchestrator.GateDecisionIntent{GateID: 1})
	resultCh := make(chan tea.Msg, 1)
	go func() { resultCh <- cmd() }()

	select {
	case <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("sendIntent with a nil channel should return immediately")
	}
}
