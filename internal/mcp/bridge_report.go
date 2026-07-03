package mcp

import (
	"encoding/json"
	"log/slog"
	"net"
)

func (b *QuestionBridge) handleReport(conn net.Conn, env bridgeEnvelope) {
	var sub ReportSubmission
	if err := json.Unmarshal(env.Payload, &sub); err != nil {
		slog.Debug("question bridge report unmarshal error", "err", err)
		return
	}

	key := env.InvocationID
	if key == "" {
		// Fail-safe: no --invocation-id was threaded through (a degraded or
		// pre-WP12 caller) — fall back to keying by agentID, and say so.
		key = env.AgentID
		slog.Debug("question bridge report envelope missing invocation_id; falling back to agent_id key", "agent_id", env.AgentID)
	}

	b.reportsMu.Lock()
	b.reports[key] = sub
	if ch, ok := b.reportWaiters[key]; ok {
		close(ch)
		delete(b.reportWaiters, key)
	}
	b.reportsMu.Unlock()

	ack, _ := json.Marshal(map[string]bool{"ok": true}) // fire-and-forget: map[string]bool marshal cannot fail
	if err := writeFrameDeadline(conn, ack, b.frameTimeout); err != nil {
		slog.Debug("question bridge write report ack error", "err", err)
	}
}

// ReportSignal arms the report-correlation slot for one agent invocation,
// identified by nonce (the value AgentSupervisor.Run injected into the
// subprocess's --invocation-id arg), and returns a channel that closes when a
// report arrives for it. Call this BEFORE starting the subprocess — there is
// no re-arming step (WP12/J34-reports): nonce is unique per invocation by
// construction, so a fresh invocation's key has never been used before and
// can never collide with a still-pending earlier one, even for the same
// agentID.
//
// It also records agentID → nonce in agentNonce so TakeReport(agentID) can
// resolve the CURRENT invocation's key without the caller ever threading a
// nonce through the ReportStore interface (see the agentNonce field comment).
//
// nonce == "" (no --invocation-id, a degraded/pre-WP12 caller) falls back to
// keying directly by agentID — the same fallback handleReport applies.
// If a report has already arrived under this key, the returned channel is
// pre-closed. Callers must not close it.
func (b *QuestionBridge) ReportSignal(agentID, nonce string) <-chan struct{} {
	key := nonce
	if key == "" {
		key = agentID
	}

	b.reportsMu.Lock()
	defer b.reportsMu.Unlock()

	b.agentNonce[agentID] = key

	if _, ok := b.reports[key]; ok {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	if ch, ok := b.reportWaiters[key]; ok {
		return ch
	}
	ch := make(chan struct{})
	b.reportWaiters[key] = ch
	return ch
}

// TakeReport returns and removes the report submitted by agentID, if any.
// The correlation key is resolved internally via agentNonce (the nonce most
// recently armed for agentID by ReportSignal) — identical to handleReport's
// storage derivation — so report harvesting never depends on a separately
// captured session ID or the caller knowing the invocation's nonce. See the
// agentNonce field comment for why this indirection exists: it is what keeps
// every pre-WP12 ReportStore.TakeReport(agentID) call site unchanged.
//
// If ReportSignal was never called for agentID (e.g. a bridge-less test, or a
// role that never arms report correlation), TakeReport falls back to keying
// directly by agentID — matching handleReport's own fallback for a
// missing-nonce envelope.
func (b *QuestionBridge) TakeReport(agentID string) (string, bool) {
	b.reportsMu.Lock()
	defer b.reportsMu.Unlock()

	key, ok := b.agentNonce[agentID]
	if !ok {
		key = agentID
	}

	sub, ok := b.reports[key]
	if !ok {
		return "", false
	}
	delete(b.reports, key)
	return sub.Report, true
}

// Release removes the report-correlation WAITER for one invocation
// (agentID, nonce) once that invocation's AgentSupervisor.Run call is
// returning — a hardening fix (refactor-review-2026-07-03.md §1): without
// it, an armed-but-never-fulfilled reportWaiters entry (an invocation that
// finished via some other path — a natural stream end, a timeout, a
// cancellation — without ever calling SubmitReport) sits in the map
// forever, since nothing else ever removes a waiter except handleReport's
// own delivery path.
//
// Release deliberately does NOT delete b.reports[key] or
// b.agentNonce[agentID]. Report harvesting (ReportHarvester.Harvest →
// TakeReport(agentID), report_harvest.go) runs in the CALLER, strictly
// AFTER AgentSupervisor.Run returns — the exact point where Release is
// deferred (see agent_supervisor.go's Run). A report that arrived moments
// before Run returned must still be resolvable by the harvester's
// TakeReport call that follows in the same goroutine; deleting
// reports/agentNonce here would race that call and could silently drop a
// just-submitted report before anything ever reads it. Only the WAITER —
// which nothing will ever read again once this invocation's Run has
// returned — is safe to clear unconditionally at this point. agentNonce is
// intentionally left alone too: it is bounded (one entry per distinct
// agentID, not per invocation, since ReportSignal overwrites it in place)
// and a later invocation of the same agentID may already have re-armed it
// with its OWN nonce by the time this (older) invocation releases —
// clobbering that would misdirect the NEWER invocation's TakeReport call.
func (b *QuestionBridge) Release(agentID, nonce string) {
	key := nonce
	if key == "" {
		key = agentID
	}
	b.reportsMu.Lock()
	defer b.reportsMu.Unlock()
	delete(b.reportWaiters, key)
}
