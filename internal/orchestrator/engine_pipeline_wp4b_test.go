package orchestrator

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/config"
	"github.com/xiii/orqestra/internal/harness"
	"github.com/xiii/orqestra/internal/mcp"
)

// --- Minimal wire-format client (mirrors internal/mcp/frame.go's private
// framing) so this orchestrator-level test can act as a real mcp-bridge
// subprocess client over the actual Unix socket, without reaching into mcp
// package internals. ---

func wp4bWriteFrame(w io.Writer, data []byte) error {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(data)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

func wp4bReadFrame(r io.Reader) ([]byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}
	return data, nil
}

type wp4bEnvelope struct {
	Kind    string          `json:"kind"`
	AgentID string          `json:"agent_id"`
	Payload json.RawMessage `json:"payload"`
}

// sendTestQuestion dials the bridge's real Unix socket and submits a question
// in the exact wire format the mcp-bridge subprocess uses, then blocks for
// the answer (delivered via bridge.SendAnswer).
func sendTestQuestion(ctx context.Context, socketPath, agentID string, tc mcp.ToolCall) (mcp.Answer, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return mcp.Answer{}, fmt.Errorf("dial bridge socket: %w", err)
	}
	defer conn.Close()

	payload, err := json.Marshal(tc)
	if err != nil {
		return mcp.Answer{}, err
	}
	envData, err := json.Marshal(wp4bEnvelope{Kind: "question", AgentID: agentID, Payload: payload})
	if err != nil {
		return mcp.Answer{}, err
	}
	if err := wp4bWriteFrame(conn, envData); err != nil {
		return mcp.Answer{}, fmt.Errorf("write question frame: %w", err)
	}

	answerData, err := wp4bReadFrame(conn)
	if err != nil {
		return mcp.Answer{}, fmt.Errorf("read answer frame: %w", err)
	}
	var answer mcp.Answer
	if err := json.Unmarshal(answerData, &answer); err != nil {
		return mcp.Answer{}, err
	}
	return answer, nil
}

// waitSocketDialable polls (bounded) until socketPath accepts a raw dial.
// QuestionBridge.Run has no readiness signal yet (deferred to WP12) — this is
// the standard bounded-retry idiom for an external OS listener coming up, not
// an unbounded sleep-based synchronization.
func waitSocketDialable(t *testing.T, socketPath string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", socketPath, 50*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s never became dialable within %s", socketPath, timeout)
}

// waitTerminal polls (bounded) until obs reports the run finished.
func waitTerminal(t *testing.T, obs *ObsStore, timeout time.Duration) ObsSnapshot {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		snap := obs.Snapshot()
		if snap.Terminal.Done {
			return snap
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not reach Terminal.Done within %s", timeout)
		}
		select {
		case <-obs.NotifyCh():
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// writeSleepyStub writes a small self-terminating script that sleeps briefly
// before exiting cleanly — long enough that a run driven by it is reliably
// still "in flight" (its forwarder still alive) when the test injects a
// question, without depending on cancellation/group-kill semantics.
func writeSleepyStub(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sleepy-stub.sh")
	script := "#!/bin/bash\nsleep 2\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub script: %v", err)
	}
	return path
}

// wp4bTestEngine builds a real Engine whose architect spec points at a stub
// binary that sleeps briefly then exits with no output. Model.Provider is set
// to "native" (no env/API-key setup required, per internal/harness's own
// role_agents_test.go pattern) so harness.Run actually execs the stub instead
// of failing at env-build time; Sandbox.RepoPath/Profiles are left zero, which
// skips sandbox wrapping entirely (exec.go), so no real sandbox-exec/claude
// dependency exists. DeliberateStep still fails afterward (no report/session
// extracted from an empty stream) and RunPipeline returns — but only once the
// stub's sleep elapses, giving this test a reliable in-flight window. This
// test exercises Engine.Start's LIFECYCLE plumbing (bridge ownership,
// forwarder join, question routing) — not deliberation correctness.
func wp4bTestEngine(t *testing.T, bridge *mcp.QuestionBridge) *Engine {
	t.Helper()
	stub := writeSleepyStub(t)

	return &Engine{
		Config:   config.DefaultConfig(),
		RepoPath: t.TempDir(),
		Specs: ProcessSpecs{
			Architect: harness.ProcessSpec{
				Binary: stub,
				Model:  harness.ModelSpec{Provider: "native"},
			},
		},
		QuestionBridge: bridge,
	}
}

// wp4bInput sets DeliberationRounds explicitly (a known-live gap, J24) so
// resolveSetup doesn't silently fall back to DefaultPipelineSetup (which
// enables Execution and a gate) — Execution:false/Validation:false alone is
// the PipelineSetup zero value.
func wp4bInput() Input {
	return Input{
		Prompt: "wp4b lifecycle probe",
		Setup: PipelineSetup{
			Execution: false, Validation: false,
			DeliberationRounds: 1,
		},
	}
}

// TestEngineStart_QuestionBridgeLifecycle is the WP4b/J5,J41 gate proof: two
// sequential Engine.Start runs on one Engine, with the QuestionBridge started
// exactly once (as tui.Run would), must (a) join run 1's question-forwarder
// before its terminal state is observable, (b) route a question arriving
// during run 2 to run 2's ObsStore — never run 1's — and (c) never rebind the
// bridge's socket.
func TestEngineStart_QuestionBridgeLifecycle(t *testing.T) {
	// A Unix domain socket path is capped at ~104 bytes (macOS sockaddr_un);
	// t.TempDir() nests under the test name and easily overflows that, so this
	// mirrors production (cmd/orqestra/main.go's buildEngine) by using /tmp
	// directly with a short, PID-unique name.
	socketPath := filepath.Join("/tmp", fmt.Sprintf("orqestra-wp4b-test-%d.sock", os.Getpid()))
	defer os.Remove(socketPath) // fire-and-forget: best-effort test cleanup
	bridge := mcp.NewQuestionBridge(socketPath)

	// Simulate tui.Run's responsibility (WP4b): the bridge starts ONCE, for
	// the whole session, on a context independent of any individual run.
	bridgeCtx, bridgeCancel := context.WithCancel(context.Background())
	defer bridgeCancel()
	bridgeErrCh := make(chan error, 1)
	go func() { bridgeErrCh <- bridge.Run(bridgeCtx) }()
	waitSocketDialable(t, socketPath, 5*time.Second)

	statBefore, err := os.Stat(socketPath)
	if err != nil {
		t.Fatalf("stat socket after initial bind: %v", err)
	}

	engine := wp4bTestEngine(t, bridge)

	// --- Run 1 ---
	run1Ctx, run1Cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer run1Cancel()
	handle1 := engine.Start(run1Ctx, wp4bInput())
	waitTerminal(t, handle1.Obs, 10*time.Second)

	// (a) run-1's forwarder must already be joined by the time its terminal
	// state is observable — otherwise an observer reacting to completion (the
	// TUI, or this test) could start run 2 while run 1's forwarder is still a
	// live consumer of the shared Questions() channel. A non-blocking receive
	// only succeeds without blocking on an already-CLOSED channel; a nil or
	// still-open channel hits default.
	select {
	case <-handle1.forwarderDone:
	default:
		t.Fatal("run-1's question-forwarder was not joined before Terminal.Done became observable (J41)")
	}

	// --- Run 2 ---
	run2Ctx, run2Cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer run2Cancel()
	handle2 := engine.Start(run2Ctx, wp4bInput())

	// (b) A question arriving while run 2 is in flight must land in run 2's
	// ObsStore — never run 1's (already-finished) store.
	answerCh := make(chan mcp.Answer, 1)
	askErrCh := make(chan error, 1)
	askCtx, askCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer askCancel()
	go func() {
		ans, askErr := sendTestQuestion(askCtx, socketPath, "architect", mcp.ToolCall{Question: "WP4b probe?"})
		if askErr != nil {
			askErrCh <- askErr
			return
		}
		answerCh <- ans
	}()

	var questionID string
	deadline := time.Now().Add(5 * time.Second)
	for {
		if snap2 := handle2.Obs.Snapshot(); snap2.HasQuestion {
			questionID = snap2.UserQuestion.ID
			break
		}
		if handle1.Obs.Snapshot().HasQuestion {
			t.Fatal("question landed in run-1's (finished) ObsStore instead of run-2's (J5/J41)")
		}
		if time.Now().After(deadline) {
			t.Fatal("question never landed in run-2's ObsStore within grace period")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Complete the round trip so the dialing goroutine above unblocks. The
	// bridge only accepts an answer whose ID matches its pending question
	// (WP5/J17) — echo the ID this test just observed in run-2's ObsStore.
	bridge.SendAnswer(mcp.Answer{ID: questionID, FreeformText: "ack"})
	select {
	case askErr := <-askErrCh:
		t.Fatalf("sendTestQuestion failed: %v", askErr)
	case <-answerCh:
	case <-time.After(5 * time.Second):
		t.Fatal("never received an answer round-trip")
	}

	waitTerminal(t, handle2.Obs, 10*time.Second)

	// (c) exactly one bound socket listener across both runs: if Engine.Start
	// had (incorrectly) re-run the bridge internally, the socket file would
	// have been removed and recreated (a new inode) at some point.
	statAfter, err := os.Stat(socketPath)
	if err != nil {
		t.Fatalf("stat socket after both runs: %v", err)
	}
	if !os.SameFile(statBefore, statAfter) {
		t.Fatal("the bridge socket was rebound during Engine.Start (Run called more than once) — J5/J41")
	}

	bridgeCancel()
	select {
	case <-bridgeErrCh:
	case <-time.After(5 * time.Second):
		t.Fatal("bridge.Run did not return after its context was cancelled")
	}
}
