package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// bridge_report_test.go holds the SubmitReport/nonce-correlation test suite
// (WP17/A8: split out of bridge_test.go, which had grown past 500 lines).
// awaitSocket/runBridge (bridge_test.go) are shared helpers.

// sendReport dials the bridge and sends a report envelope, returning the ack.
func sendReport(sockPath, agentID, report, summary string) error {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	payload, _ := json.Marshal(ReportSubmission{Report: report, Summary: summary})
	env := bridgeEnvelope{Kind: "report", AgentID: agentID, Payload: payload}
	envData, _ := json.Marshal(env)
	if err := writeFrame(conn, envData); err != nil {
		return err
	}
	ackData, err := readFrame(conn)
	if err != nil {
		return err
	}
	var ack struct {
		OK bool `json:"ok"`
	}
	return json.Unmarshal(ackData, &ack)
}

func TestQuestionBridge_ReportRoundTrip(t *testing.T) {
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-report-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runBridge(t, ctx, bridge, sockPath)

	const agentID = "researcher"
	const reportText = "## Goal\nTest report.\n## Codebase Facts\n- fact1"

	if err := sendReport(sockPath, agentID, reportText, "test summary"); err != nil {
		t.Fatalf("sendReport: %v", err)
	}

	// TakeReport should return the report and remove it.
	got, ok := bridge.TakeReport(agentID)
	if !ok {
		t.Fatal("expected TakeReport to return true")
	}
	if got != reportText {
		t.Errorf("report = %q, want %q", got, reportText)
	}

	// Second call should return nothing.
	_, ok = bridge.TakeReport(agentID)
	if ok {
		t.Error("expected TakeReport to return false on second call")
	}
}

// sendReportNonce dials the bridge and sends a report envelope carrying an
// InvocationID (WP12/J34-reports), returning the ack.
func sendReportNonce(sockPath, agentID, nonce, report, summary string) error {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	payload, _ := json.Marshal(ReportSubmission{Report: report, Summary: summary})
	env := bridgeEnvelope{Kind: "report", AgentID: agentID, InvocationID: nonce, Payload: payload}
	envData, _ := json.Marshal(env)
	if err := writeFrame(conn, envData); err != nil {
		return err
	}
	ackData, err := readFrame(conn)
	if err != nil {
		return err
	}
	var ack struct {
		OK bool `json:"ok"`
	}
	return json.Unmarshal(ackData, &ack)
}

func TestQuestionBridge_TakeReport_ByAgentID_AfterNonceArmed(t *testing.T) {
	// WP12/J34-reports: ReportSignal(agentID, nonce) records agentID → nonce
	// internally so TakeReport(agentID) — the unchanged ReportStore interface
	// every call site uses — resolves the CURRENT invocation's nonce-keyed
	// report without ever threading a nonce through step.go/report_harvest.go.
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-nonce-corr-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runBridge(t, ctx, bridge, sockPath)

	const agentID = "architect"
	const nonce = "inv-1234-1"
	const reportText = "## Goal\nShip it.\n## Work Packages\n- one"

	// The supervisor arms BEFORE starting the subprocess — no session
	// correlation, no re-arming.
	sig := bridge.ReportSignal(agentID, nonce)

	if err := sendReportNonce(sockPath, agentID, nonce, reportText, ""); err != nil {
		t.Fatalf("sendReportNonce: %v", err)
	}

	select {
	case <-sig:
	case <-time.After(2 * time.Second):
		t.Fatal("ReportSignal(agentID, nonce) did not fire after a report stored under the nonce key")
	}

	// Harvest by agentID — resolves agentNonce[agentID] internally; no
	// RunResult.SessionID or nonce needed by the caller.
	got, ok := bridge.TakeReport(agentID)
	if !ok {
		t.Fatal("expected TakeReport(agentID) to find the nonce-keyed report")
	}
	if got != reportText {
		t.Errorf("report = %q, want %q", got, reportText)
	}
}

func TestQuestionBridge_TakeReport_FallsBackToAgentIDWithoutArming(t *testing.T) {
	// If ReportSignal was never called for agentID (no supervisor arm — e.g.
	// a bridge-less test, or an old-style envelope with no InvocationID),
	// TakeReport(agentID) must still resolve via the agentID fallback key,
	// matching handleReport's own missing-nonce fallback.
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-nonce-fallback-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runBridge(t, ctx, bridge, sockPath)

	if err := sendReport(sockPath, "critic", "stale report", ""); err != nil {
		t.Fatalf("sendReport: %v", err)
	}
	got, ok := bridge.TakeReport("critic")
	if !ok {
		t.Fatal("expected TakeReport to fall back to the agentID key when unarmed")
	}
	if got != "stale report" {
		t.Errorf("report = %q, want %q", got, "stale report")
	}

	// A second, un-taken delivery followed by a fresh TakeReport must return
	// exactly that new report — no stale-clearing step exists (or is needed)
	// with nonce correlation: each invocation's key is unique, so nothing
	// ever needs to be manually cleared.
	if err := sendReport(sockPath, "critic", "stale report 2", ""); err != nil {
		t.Fatalf("sendReport 2: %v", err)
	}
	got2, ok := bridge.TakeReport("critic")
	if !ok {
		t.Fatal("expected second report to be stored")
	}
	if got2 != "stale report 2" {
		t.Errorf("report = %q, want %q", got2, "stale report 2")
	}
}

func TestReportSignal_OpenBeforeDelivery(t *testing.T) {
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-sig-open-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runBridge(t, ctx, bridge, sockPath)

	const nonce = "inv-open-1"
	sig := bridge.ReportSignal("architect", nonce)

	// Signal must be open (not yet closed) before delivery.
	select {
	case <-sig:
		t.Fatal("signal should not be closed before report delivery")
	default:
	}

	// Deliver the report.
	if err := sendReportNonce(sockPath, "architect", nonce, "# Plan\n## Goal\nTest.", ""); err != nil {
		t.Fatalf("sendReportNonce: %v", err)
	}

	// Signal must close promptly after delivery.
	select {
	case <-sig:
	case <-time.After(time.Second):
		t.Fatal("signal did not close after report delivery")
	}
}

func TestReportSignal_PreClosedWhenReportExists(t *testing.T) {
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-sig-pre-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runBridge(t, ctx, bridge, sockPath)

	const nonce = "inv-pre-1"
	// Deliver the report first.
	if err := sendReportNonce(sockPath, "researcher", nonce, "## Goal\nTest.", ""); err != nil {
		t.Fatalf("sendReportNonce: %v", err)
	}

	// ReportSignal called after delivery must return a pre-closed channel.
	sig := bridge.ReportSignal("researcher", nonce)
	select {
	case <-sig:
	default:
		t.Fatal("signal should be pre-closed when report already exists")
	}
}

func TestReportSignal_TwoInvocationsSameAgentID_DoNotCollide(t *testing.T) {
	// WP12/J34-reports gate (c): two sequential invocations of the SAME
	// agentID — the exact scenario that collided under the old session-based
	// correlation (RegisterSession(agentID, "") re-armed the SAME fallback
	// key for both invocations whenever no session ID was known yet) — must
	// each get their own report via a distinct nonce, with no cross-talk.
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-sig-two-inv-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runBridge(t, ctx, bridge, sockPath)

	const agentID = "architect"
	const nonce1 = "inv-100-1"
	const nonce2 = "inv-100-2"

	// Invocation 1 arms first (matches the supervisor arming before starting
	// its subprocess).
	sig1 := bridge.ReportSignal(agentID, nonce1)

	// Invocation 2 starts before invocation 1's report arrives (e.g.
	// invocation 1 is slow) and arms its OWN nonce — never touching
	// invocation 1's slot.
	sig2 := bridge.ReportSignal(agentID, nonce2)

	// Invocation 1's report arrives, late.
	if err := sendReportNonce(sockPath, agentID, nonce1, "invocation-1 report", ""); err != nil {
		t.Fatalf("sendReportNonce 1: %v", err)
	}

	select {
	case <-sig1:
	case <-time.After(time.Second):
		t.Fatal("invocation-1's own signal did not fire from its own report")
	}
	// Invocation 2's signal must NOT have fired — no cross-invocation collision.
	select {
	case <-sig2:
		t.Fatal("invocation-2's ReportSignal incorrectly fired from invocation-1's report")
	default:
	}

	// agentNonce currently points at nonce2 (the LAST arm) — TakeReport(agentID)
	// resolves invocation 2's key, which has no report yet.
	if _, ok := bridge.TakeReport(agentID); ok {
		t.Fatal("TakeReport(agentID) should resolve invocation-2's (still empty) slot, not invocation-1's stray report")
	}

	// Invocation 2's own report arrives and is retrievable, uncontaminated.
	if err := sendReportNonce(sockPath, agentID, nonce2, "invocation-2 report", ""); err != nil {
		t.Fatalf("sendReportNonce 2: %v", err)
	}
	select {
	case <-sig2:
	case <-time.After(time.Second):
		t.Fatal("invocation-2's signal did not fire from its own report")
	}
	got, ok := bridge.TakeReport(agentID)
	if !ok {
		t.Fatal("expected invocation-2's report to be retrievable")
	}
	if got != "invocation-2 report" {
		t.Errorf("report = %q, want %q", got, "invocation-2 report")
	}
}

func TestReportSignal_SameKeyReuseNoDoubleClosePanic(t *testing.T) {
	// Defensive robustness: even if a key were ever reused (nonces are unique
	// per invocation by construction, so this should not happen operationally),
	// arm → deliver → take → re-arm-same-key → deliver again must never panic
	// from a double-close of the waiter channel.
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-sig-dup-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runBridge(t, ctx, bridge, sockPath)

	const agentID = "critic"
	const nonce = "inv-dup-1"

	sig := bridge.ReportSignal(agentID, nonce)
	if err := sendReportNonce(sockPath, agentID, nonce, "## Critic Report\n### Blockers Found\nNone.", ""); err != nil {
		t.Fatalf("sendReportNonce 1: %v", err)
	}
	select {
	case <-sig:
	case <-time.After(time.Second):
		t.Fatal("signal did not close after first delivery")
	}
	bridge.TakeReport(agentID)

	// Re-arm the SAME key. Must not panic even though delivery closes a
	// brand-new channel this time (the old one was already deleted from
	// reportWaiters by handleReport, so there is nothing to double-close).
	sig2 := bridge.ReportSignal(agentID, nonce)
	if err := sendReportNonce(sockPath, agentID, nonce, "## Critic Report\n### Blockers Found\nNone again.", ""); err != nil {
		t.Fatalf("sendReportNonce 2: %v", err)
	}
	select {
	case <-sig2:
	case <-time.After(time.Second):
		t.Fatal("second signal did not close")
	}
	got, ok := bridge.TakeReport(agentID)
	if !ok {
		t.Fatal("expected second report to be stored")
	}
	if got == "" {
		t.Error("second report text is empty")
	}
}

// TestQuestionBridge_Release_RemovesUnfulfilledWaiterOnly is the WP17
// bridge-hygiene gate: Release(agentID, nonce) must remove an
// armed-but-never-fulfilled reportWaiters entry (the actual leak — nothing
// else ever cleans one up), while leaving a report that already arrived
// (and its agentNonce mapping) alone, since ReportHarvester.Harvest calls
// TakeReport strictly AFTER Release runs (both happen at/after
// AgentSupervisor.Run's return).
func TestQuestionBridge_Release_RemovesUnfulfilledWaiterOnly(t *testing.T) {
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-release-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runBridge(t, ctx, bridge, sockPath)

	const agentID = "worker"
	const nonce = "inv-release-1"

	// Arm, but never deliver a report — the common "agent finished without
	// ever calling SubmitReport" case.
	sig := bridge.ReportSignal(agentID, nonce)

	bridge.Release(agentID, nonce)

	// The waiter must be gone: proven indirectly — a report arriving AFTER
	// Release must not find (and close) a waiter channel that no longer
	// exists, and TakeReport must still resolve it via agentNonce (left
	// alone by Release) once the report lands.
	if err := sendReportNonce(sockPath, agentID, nonce, "late report after release", ""); err != nil {
		t.Fatalf("sendReportNonce: %v", err)
	}

	// sig itself is now orphaned (nobody will ever read it again in
	// production — Release fires right as AgentSupervisor.Run returns,
	// after which nothing reads reportSig again) — it must NOT have been
	// closed by the late report, since Release already removed it from
	// reportWaiters before the report arrived.
	select {
	case <-sig:
		t.Error("the released waiter channel was closed by a report delivered after Release — reportWaiters entry was not removed")
	default:
	}

	// The report itself, and agentNonce, must still be intact — a fresh
	// TakeReport(agentID) (mirroring the harvester's call right after Run
	// returns) must still find it.
	got, ok := bridge.TakeReport(agentID)
	if !ok {
		t.Fatal("expected the late report to still be retrievable after Release — Release must not touch reports/agentNonce")
	}
	if got != "late report after release" {
		t.Errorf("report = %q, want %q", got, "late report after release")
	}
}

// TestQuestionBridge_Release_DoesNotClobberNewerInvocationsNonce covers the
// ordering hazard explicitly called out in Release's doc comment: an OLDER
// invocation releasing after a NEWER one has already re-armed the same
// agentID must never disturb the newer invocation's agentNonce mapping.
func TestQuestionBridge_Release_DoesNotClobberNewerInvocationsNonce(t *testing.T) {
	sockPath := filepath.Join("/tmp", fmt.Sprintf("orq-test-release-order-%d.sock", os.Getpid()))
	defer os.Remove(sockPath)
	bridge := NewQuestionBridge(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runBridge(t, ctx, bridge, sockPath)

	const agentID = "architect"
	const nonce1 = "inv-release-old"
	const nonce2 = "inv-release-new"

	bridge.ReportSignal(agentID, nonce1)
	bridge.ReportSignal(agentID, nonce2) // newer invocation re-arms first

	// The OLDER invocation releases late (its Run call happened to return
	// after the newer one had already re-armed).
	bridge.Release(agentID, nonce1)

	// The newer invocation's report must still resolve via TakeReport(agentID).
	if err := sendReportNonce(sockPath, agentID, nonce2, "newer invocation report", ""); err != nil {
		t.Fatalf("sendReportNonce: %v", err)
	}
	got, ok := bridge.TakeReport(agentID)
	if !ok {
		t.Fatal("expected the newer invocation's report to be retrievable")
	}
	if got != "newer invocation report" {
		t.Errorf("report = %q, want %q", got, "newer invocation report")
	}
}
