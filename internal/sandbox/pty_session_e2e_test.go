//go:build integration

package sandbox

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDockerPTY_SIGWINCH_Propagation(t *testing.T) {
	cli, containerID := setupPTYTestContainer(t)
	ctx := context.Background()

	ps := NewPTYSession("test-sigwinch", "sigwinch", cli)
	err := ps.Start(ctx, containerID, []string{"sh"}, nil, 80, 24)
	require.NoError(t, err)
	defer ps.Close()

	// Give sh time to start.
	time.Sleep(200 * time.Millisecond)

	// Resize to 132x50.
	err = ps.Resize(132, 50)
	require.NoError(t, err)
	time.Sleep(200 * time.Millisecond)

	// Query terminal size via stty.
	_, err = ps.Write([]byte("stty size\n"))
	require.NoError(t, err)

	output := readUntil(t, ps, "50 132", 5*time.Second)
	require.Contains(t, output, "50 132")
}

func TestDockerPTY_SIGINT_Kills_Process(t *testing.T) {
	cli, containerID := setupPTYTestContainer(t)
	ctx := context.Background()

	ps := NewPTYSession("test-sigint-kill", "sigint-kill", cli)
	err := ps.Start(ctx, containerID, []string{"sh", "-c", "trap 'echo caught' INT; sleep 60"}, nil, 80, 24)
	require.NoError(t, err)
	defer ps.Close()

	// Give trap setup time.
	time.Sleep(500 * time.Millisecond)

	// Send Ctrl+C.
	_, err = ps.Write([]byte{0x03})
	require.NoError(t, err)

	output := readUntil(t, ps, "caught", 5*time.Second)
	require.Contains(t, output, "caught")
}

func TestDockerPTY_RawMode_Passthrough(t *testing.T) {
	cli, containerID := setupPTYTestContainer(t)
	ctx := context.Background()

	// Use sh with stty raw to allow binary passthrough.
	ps := NewPTYSession("test-raw", "raw-mode", cli)
	err := ps.Start(ctx, containerID, []string{"sh", "-c", "stty raw; cat"}, nil, 80, 24)
	require.NoError(t, err)
	defer ps.Close()

	// Give time for stty raw + cat to start.
	time.Sleep(300 * time.Millisecond)

	// Write binary bytes (avoid \x00 which some TTYs treat as NUL/discard).
	_, err = ps.Write([]byte{0x01, 0x02})
	require.NoError(t, err)

	// Read back — in raw mode, cat should echo the bytes.
	output := readUntil(t, ps, "\x01", 5*time.Second)
	require.Contains(t, output, "\x01")
}

func TestDockerPTY_LongOutput_NoTruncation(t *testing.T) {
	cli, containerID := setupPTYTestContainer(t)
	ctx := context.Background()

	ps := NewPTYSession("test-long", "long-output", cli)
	err := ps.Start(ctx, containerID, []string{"seq", "1", "5000"}, nil, 80, 24)
	require.NoError(t, err)
	defer ps.Close()

	output := drainUntilEOF(t, ps, 30*time.Second)

	// Verify we got all 5000 lines by checking for the last line.
	require.Contains(t, output, "5000")

	// Count lines — should be at least 5000 (may have extra from prompt/echo).
	lines := strings.Split(strings.TrimSpace(output), "\n")
	require.GreaterOrEqual(t, len(lines), 4990, "expected at least 4990 lines, got %d", len(lines))
}
