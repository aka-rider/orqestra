package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/mcp"
)

// TestEngineStart_ProceedsWhenBridgeNeverBecomesReady is the WP12/J36
// bounded-degradation proof: if the bridge is present but its Run(ctx) is
// never (or not yet) invoked — so Ready() never closes — startNew's
// readiness wait must NOT hang the run forever. It proceeds after
// bridgeReadyTimeout, logged, exactly like a nil bridge. This is the
// complement to internal/mcp's TestWP12_DialNeverRefusedAfterReady (which
// proves the positive case: dialing right after Ready() closes never sees
// ECONNREFUSED) — here the bridge deliberately never reaches Ready() at all.
func TestEngineStart_ProceedsWhenBridgeNeverBecomesReady(t *testing.T) {
	socketPath := filepath.Join("/tmp", fmt.Sprintf("orqestra-wp12-neverready-%d.sock", os.Getpid()))
	defer os.Remove(socketPath) // fire-and-forget: best-effort test cleanup
	bridge := mcp.NewQuestionBridge(socketPath)
	// Deliberately never call bridge.Run(ctx) — Ready() can never close.

	engine := wp4bTestEngine(t, bridge)

	start := time.Now()
	handle := engine.Start(t.Context(), wp4bInput())
	// Bounded generously: bridgeReadyTimeout (2s) for the readiness wait, plus
	// the sleepy stub's own ~2s sleep before DeliberateStep fails and the
	// pipeline concludes.
	_, _ = waitRunFinished(t, handle.Events, 10*time.Second)
	elapsed := time.Since(start)

	if elapsed < bridgeReadyTimeout {
		t.Fatalf("run finished in %s, faster than bridgeReadyTimeout (%s) — the readiness wait did not run", elapsed, bridgeReadyTimeout)
	}
}
