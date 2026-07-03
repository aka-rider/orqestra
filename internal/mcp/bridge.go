package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
)

// bridgeEnvelope is the framed wire format for bridge messages.
// Kind is "question" or "report"; AgentID identifies the sending role.
// InvocationID (WP12/J34-reports) is the per-invocation nonce
// AgentSupervisor.Run generates and injects into the "orqestra" inline MCP
// server's --invocation-id arg; it is the report/question correlation key.
// Empty when the subprocess was launched without the flag (no Inline
// "orqestra" entry, or an older/degraded caller) — handleReport falls back to
// keying by AgentID in that case, logged as a fail-safe degradation.
type bridgeEnvelope struct {
	Kind         string          `json:"kind"`
	AgentID      string          `json:"agent_id"`
	InvocationID string          `json:"invocation_id,omitempty"`
	Payload      json.RawMessage `json:"payload"`
}

// ReportSubmission holds a report delivered by an agent via SubmitReport.
type ReportSubmission struct {
	Report  string `json:"report"`
	Summary string `json:"summary,omitempty"`
}

// QuestionBridge listens on a Unix socket for questions from MCP bridge
// subprocesses and routes them to the orchestrator via channels.
//
// Flow: MCP server (subprocess) → Unix socket → QuestionBridge → channel → orchestrator → TUI
//
//	TUI answer → orchestrator → SendAnswer() → channel → QuestionBridge → socket → MCP server
//
// Lifetime: call Run(ctx) exactly ONCE, for the whole engine/TUI-session
// lifetime (WP4b/J5,J41) — not per pipeline run.  It blocks until ctx is
// cancelled and then cleans up.
//
// Report correlation (WP12/RC3,J34-reports): reports and their waiters are
// keyed by the per-invocation nonce AgentSupervisor.Run generates and arms
// via ReportSignal BEFORE starting each subprocess — never by session ID or
// agentID alone. Because every invocation gets a fresh, unique nonce, two
// invocations of the same agentID (sequential retries, or a role with no
// session ID at all) can never collide, and there is no re-arming step: a new
// invocation's key has never been used before.
type QuestionBridge struct {
	socketPath    string
	questions     chan ToolCall
	pendingAnswer chan Answer
	questionSeq   atomic.Uint64 // generates unique ToolCall.ID values (WP5/J17,J25)

	reportsMu     sync.Mutex
	reports       map[string]ReportSubmission // key: invocation nonce (fallback agentID) → report
	reportWaiters map[string]chan struct{}    // key → signal channel
	// agentNonce resolves an agentID to the nonce (or fallback key) most
	// recently armed for it via ReportSignal. It exists SOLELY so
	// TakeReport(agentID) — the ReportStore interface every existing call
	// site (step.go, report_harvest.go) already uses — keeps working
	// unchanged: those callers know their agentID but not the nonce
	// AgentSupervisor.Run generated for the invocation currently in flight.
	// ReportSignal updates this mapping at arm time, which always happens
	// BEFORE that agentID's subprocess can possibly submit a report, so
	// TakeReport always resolves the CURRENT invocation's key.
	agentNonce map[string]string

	// ready is closed once the Unix listener is bound (WP12/J36) — before
	// Run() begins ready to accept connections. Callers that must not race
	// the bind (an agent dialing immediately after starting the bridge)
	// wait on Ready().
	ready chan struct{}
}

// NewQuestionBridge creates a bridge that will listen on the given socket path.
func NewQuestionBridge(socketPath string) *QuestionBridge {
	return &QuestionBridge{
		socketPath:    socketPath,
		questions:     make(chan ToolCall, 1),
		pendingAnswer: make(chan Answer, 1),
		reports:       make(map[string]ReportSubmission),
		reportWaiters: make(map[string]chan struct{}),
		agentNonce:    make(map[string]string),
		ready:         make(chan struct{}),
	}
}

// Run starts listening on the Unix socket and blocks until ctx is cancelled.
// It cleans up the socket on return. The listener is bound SYNCHRONOUSLY,
// before Ready() closes and before the accept loop starts (WP12/J36) — a
// caller that waits on Ready() before dialing or launching an agent can never
// see ECONNREFUSED from a bind that hasn't happened yet.
func (b *QuestionBridge) Run(ctx context.Context) error {
	_ = os.Remove(b.socketPath) // fire-and-forget: stale socket from a prior run

	ln, err := net.Listen("unix", b.socketPath)
	if err != nil {
		return fmt.Errorf("listen unix %s: %w", b.socketPath, err)
	}
	defer func() { _ = os.Remove(b.socketPath) }() // fire-and-forget: best-effort cleanup
	defer ln.Close()

	close(b.ready)

	// Close listener when ctx is cancelled to unblock any in-progress ln.Accept().
	stopAfter := context.AfterFunc(ctx, func() { ln.Close() })
	defer stopAfter()

	b.acceptLoop(ctx, ln)
	return nil
}

// Ready returns a channel that is closed once the bridge's Unix listener is
// bound and accepting connections (WP12/J36). Waiting on this before dialing
// — or before launching the first agent that will dial — removes the
// bind-vs-dial race that otherwise requires bounded polling.
func (b *QuestionBridge) Ready() <-chan struct{} {
	return b.ready
}

func (b *QuestionBridge) acceptLoop(ctx context.Context, ln net.Listener) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				slog.Debug("question bridge accept error", "err", err)
				continue
			}
		}

		// Per-connection goroutine (WP12/J16): a blocking AskUserQuestion
		// exchange on one connection must never delay a SubmitReport (or any
		// other message) arriving on a different connection. The accept loop
		// itself stays single-threaded and non-blocking — only handling the
		// connection's request/response is concurrent.
		go b.handleConnection(ctx, conn)
	}
}

func (b *QuestionBridge) handleConnection(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	data, err := readFrame(conn)
	if err != nil {
		slog.Debug("question bridge read error", "err", err)
		return
	}

	var env bridgeEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		slog.Debug("question bridge envelope unmarshal error", "err", err)
		return
	}

	switch env.Kind {
	case "question":
		b.handleQuestion(ctx, conn, env)
	case "report":
		b.handleReport(conn, env)
	default:
		slog.Debug("question bridge unknown envelope kind", "kind", env.Kind)
	}
}

func (b *QuestionBridge) handleQuestion(ctx context.Context, conn net.Conn, env bridgeEnvelope) {
	var question ToolCall
	if err := json.Unmarshal(env.Payload, &question); err != nil {
		slog.Debug("question bridge question unmarshal error", "err", err)
		return
	}
	// Generate a unique ID BEFORE forwarding (WP5/J17,J25): this is the only
	// question this call will ever accept an answer for.
	question.ID = fmt.Sprintf("q-%d", b.questionSeq.Add(1))

	select {
	case b.questions <- question:
	case <-ctx.Done():
		return
	}

	// Wait for an answer whose ID matches THIS question. SendAnswer is a
	// non-blocking send into a cap-1 buffer that nothing drains when no
	// question is pending, so a stale/double-submitted answer (J17) can sit
	// there from before this question was even asked — accepting it on ID
	// mismatch would silently misanswer this question. Drop it and keep
	// waiting for the real one.
	var answer Answer
	for {
		select {
		case answer = <-b.pendingAnswer:
		case <-ctx.Done():
			answer = Answer{Skipped: true}
		}
		if ctx.Err() != nil || answer.ID == question.ID {
			break
		}
		slog.Debug("question bridge dropped stale/mismatched answer",
			"want_id", question.ID, "got_id", answer.ID)
	}

	answerData, err := json.Marshal(answer)
	if err != nil {
		slog.Debug("question bridge marshal answer error", "err", err)
		return
	}
	if err := writeFrame(conn, answerData); err != nil {
		slog.Debug("question bridge write answer error", "err", err)
	}
}

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
	if err := writeFrame(conn, ack); err != nil {
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

// SocketPath returns the Unix socket path for MCP config injection.
func (b *QuestionBridge) SocketPath() string {
	return b.socketPath
}

// Questions returns the channel that receives questions from MCP bridge subprocesses.
func (b *QuestionBridge) Questions() <-chan ToolCall {
	return b.questions
}

// SendAnswer delivers a user's answer back to the waiting MCP bridge subprocess.
// Non-blocking: drops the answer if no question is pending (bridge not running
// or connection already dropped).
func (b *QuestionBridge) SendAnswer(answer Answer) {
	select {
	case b.pendingAnswer <- answer:
	default:
	}
}
