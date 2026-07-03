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
	"time"
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

// connFrameTimeout bounds a single frame read/write on an accepted
// connection (WP17/A7 — "readFrame has no deadline, a hung peer leaks past
// Run's return"). Generous enough for a SubmitReport payload approaching
// maxFrameBytes (frame.go) over a local Unix socket, which transfers in low
// milliseconds even under load — 30s means only a genuinely stuck/dead peer
// ever trips it.
const connFrameTimeout = 30 * time.Second

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
//
// Question correlation (WP17/F2): each in-flight question gets its own
// dedicated answer channel, keyed by ToolCall.ID, in waiters — see the field
// doc for why this replaces a single shared answer slot.
type QuestionBridge struct {
	socketPath  string
	questions   chan ToolCall
	questionSeq atomic.Uint64 // generates unique ToolCall.ID values (WP5/J17,J25)

	// waitersMu/waiters (WP17/F2) — one dedicated, cap-1 Answer channel per
	// in-flight question, keyed by ToolCall.ID. This replaces the pre-WP17
	// shared cap-1 pendingAnswer channel: with a single shared slot, a
	// zombie handleQuestion goroutine (its agent/connection already dead,
	// still blocked waiting) and the CURRENT question's real handler were
	// both reading from the same channel — an answer meant for the current
	// question could be consumed by the zombie on an ID mismatch and
	// silently dropped there (never re-queued), so the real waiter never
	// saw it (F2's "theft"). With per-question channels, SendAnswer routes
	// directly to the matching waiter by ID; there is no shared slot left
	// to steal from, and a stale/unknown ID is simply dropped at the
	// router (logged), never handed to some unrelated question.
	waitersMu sync.Mutex
	waiters   map[string]chan Answer

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

	// connWG tracks every per-connection goroutine spawned by acceptLoop
	// (WP17/A7): Run() joins it before returning, so no connection handler
	// can ever outlive the bridge itself — closing the "bridge per-connection
	// goroutines unjoined" gap (readFrame's new deadline, above, closes the
	// complementary "hung peer" half of the same finding).
	connWG sync.WaitGroup

	// frameTimeout bounds a single connection's frame read/write (defaults
	// to connFrameTimeout). Unexported: white-box test instrumentation
	// only, letting bridge_wp17_test.go exercise the hung-peer deadline
	// path deterministically without waiting out the real 30s production
	// bound. Set (if at all) BEFORE Run() is called — every reader is
	// spawned by Run/acceptLoop, so there is no concurrent-mutation window.
	frameTimeout time.Duration
}

// NewQuestionBridge creates a bridge that will listen on the given socket path.
func NewQuestionBridge(socketPath string) *QuestionBridge {
	return &QuestionBridge{
		socketPath:    socketPath,
		questions:     make(chan ToolCall, 1),
		waiters:       make(map[string]chan Answer),
		reports:       make(map[string]ReportSubmission),
		reportWaiters: make(map[string]chan struct{}),
		agentNonce:    make(map[string]string),
		ready:         make(chan struct{}),
		frameTimeout:  connFrameTimeout,
	}
}

// Run starts listening on the Unix socket and blocks until ctx is cancelled.
// It cleans up the socket on return. The listener is bound SYNCHRONOUSLY,
// before Ready() closes and before the accept loop starts (WP12/J36) — a
// caller that waits on Ready() before dialing or launching an agent can never
// see ECONNREFUSED from a bind that hasn't happened yet. Run does not return
// until every per-connection goroutine it ever spawned has also returned
// (WP17/A7).
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
	b.connWG.Wait() // WP17/A7: join every per-connection goroutine before Run returns.
	return nil
}

// Ready returns a channel that is closed once the bridge's Unix listener is
// bound and accepting connections (WP12/J36). Waiting on this before dialing
// — or before launching the first agent that will dial — removes the
// bind-vs-dial race that otherwise requires bounded polling.
func (b *QuestionBridge) Ready() <-chan struct{} {
	return b.ready
}

// StartBridgeAsync starts bridge.Run(ctx) in its own goroutine, scoped to
// ctx's lifetime, and logs (never returns) a failure — a bridge that never
// binds degrades question support rather than failing the caller (root
// CLAUDE.md §5.3). A nil bridge is a no-op.
//
// This is the ONE place the "start once, for this caller's whole lifetime"
// wiring is defined (WP16): both tui.Run (whole TUI-session lifetime) and the
// cmd/orqestra headless entry point (single-run lifetime) call this instead
// of duplicating the goroutine + nil-guard + log line.
func StartBridgeAsync(ctx context.Context, bridge *QuestionBridge) {
	if bridge == nil {
		return
	}
	go func() {
		if err := bridge.Run(ctx); err != nil {
			slog.Warn("question bridge", "err", err)
		}
	}()
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
		// connection's request/response is concurrent. Tracked in connWG
		// (WP17/A7) so Run() can join every one of these before it returns.
		b.connWG.Add(1)
		go func() {
			defer b.connWG.Done()
			b.handleConnection(ctx, conn)
		}()
	}
}

func (b *QuestionBridge) handleConnection(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	// WP17/A7: bounded — a peer that connects but never completes sending a
	// frame (a hung/dead process, or a misbehaving client) is torn down by
	// this deadline instead of leaking the connection (and this goroutine)
	// past Run's return.
	data, err := readFrameDeadline(conn, b.frameTimeout)
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

// SocketPath returns the Unix socket path for MCP config injection.
func (b *QuestionBridge) SocketPath() string {
	return b.socketPath
}

// Questions returns the channel that receives questions from MCP bridge subprocesses.
func (b *QuestionBridge) Questions() <-chan ToolCall {
	return b.questions
}
