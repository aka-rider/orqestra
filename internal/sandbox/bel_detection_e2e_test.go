//go:build integration

package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/require"
)

// TestBELDetection_ClaudeCode_E2E is the PoC test for hook-based BEL (\x07) detection.
//
// It provisions a real orqestra-sandbox container with Claude Code installed,
// which has .claude/settings.json baked in with a Stop hook that emits BEL to
// /dev/tty. The test launches Claude Code in non-interactive (-p) mode via
// PTYSession and scans the raw byte stream for BEL signals.
//
// CONFIRMED: Stop hook BEL flows through docker exec PTY and is detectable.
// The hook-generated BEL appears as raw \x07 at stream start, distinguishable
// from OSC sequence terminators (\x1b]...\x07).
//
// Requirements:
//   - Docker running
//   - orqestra-sandbox:latest image built (with hooks baked in)
//   - Ollama server at 192.168.50.212:11434 with Anthropic-compatible endpoint
//
// Run: go test ./internal/sandbox -tags integration -run TestBELDetection -v -timeout 180s
func TestBELDetection_ClaudeCode_E2E(t *testing.T) {
	cli, err := newDockerClient()
	require.NoError(t, err, "creating docker client")

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	// Create container from orqestra-sandbox image with host network.
	containerCfg := &container.Config{
		Image:     "orqestra-sandbox:latest",
		Tty:       true,
		OpenStdin: true,
		Env: []string{
			"ANTHROPIC_BASE_URL=http://192.168.50.212:11434",
			"ANTHROPIC_API_KEY=sk-ant-api03-test",
			"DISABLE_NON_ESSENTIAL_MODEL_CALLS=1",
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		},
	}
	initTrue := true
	hostCfg := &container.HostConfig{
		Init:        &initTrue,
		NetworkMode: "host",
	}

	t.Log("Creating sandbox container...")
	resp, err := cli.ContainerCreate(ctx, containerCfg, hostCfg, nil, nil, "")
	require.NoError(t, err, "creating container")
	containerID := resp.ID

	t.Cleanup(func() {
		_ = cli.ContainerRemove(context.Background(), containerID, container.RemoveOptions{Force: true})
	})

	err = cli.ContainerStart(ctx, containerID, container.StartOptions{})
	require.NoError(t, err, "starting container")
	t.Logf("Container started: %s", containerID[:12])

	// Give entrypoint time to finish.
	time.Sleep(2 * time.Second)

	// Use -p mode (non-interactive) which bypasses onboarding dialogs entirely.
	// The Stop hook in .claude/settings.json should emit BEL when Claude finishes.
	ps := NewPTYSession("bel-poc", "claude-bel", cli)
	cmd := []string{
		"claude", "-p", "What is 2+2? Reply with just the number.",
		"--dangerously-skip-permissions",
		"--max-turns", "5",
	}
	env := []string{
		"ANTHROPIC_BASE_URL=http://192.168.50.212:11434",
		"ANTHROPIC_API_KEY=sk-ant-api03-test",
		"DISABLE_NON_ESSENTIAL_MODEL_CALLS=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"HOME=/home/sandbox",
		"PATH=/home/sandbox/.bun/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"TERM=xterm-256color",
	}

	t.Log("Starting PTY session with Claude Code (-p mode)...")
	err = ps.Start(ctx, containerID, cmd, env, 120, 40)
	require.NoError(t, err, "starting PTY session")
	defer ps.Close()

	// Single persistent reader goroutine — all data goes through this channel.
	dataCh := make(chan []byte, 512)
	errCh := make(chan error, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, rerr := ps.Read(buf)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, buf[:n])
				dataCh <- cp
			}
			if rerr != nil {
				errCh <- rerr
				return
			}
		}
	}()

	// Read all output until Claude Code finishes (PTY EOF or idle timeout).
	// -p mode processes the prompt and exits. The Stop hook fires BEL on completion.
	t.Log("Reading Claude Code output (up to 120s, idle=15s)...")
	output, bels := drainUntilIdle(t, dataCh, errCh, 120*time.Second, 15*time.Second)
	t.Logf("Output length: %d bytes", len(output))
	t.Logf("BEL signals detected: %d (positions: %v)", len(bels), bels)

	// Log printable output.
	printable := stripESC(string(output))
	t.Logf("Output (printable, first 500): %s", truncate(printable, 500))
	t.Logf("Output (printable, last 500): %s", lastN(printable, 500))

	if len(output) > 0 {
		hexLen := 200
		if len(output) < hexLen {
			hexLen = len(output)
		}
		t.Logf("First %d bytes hex: %x", hexLen, output[:hexLen])
	}

	if len(bels) > 0 {
		t.Log("SUCCESS: BEL detected in Claude Code output")
		logBELContext(t, output, bels)
	}

	// Summary
	t.Log("\n=== HOOK-BASED BEL DETECTION POC RESULTS ===")
	t.Log("Strategy: .claude/settings.json Stop hook → printf '\\x07' > /dev/tty")
	t.Logf("Mode: non-interactive (-p)")
	t.Logf("BEL signals: %d", len(bels))
	t.Logf("Output bytes: %d", len(output))

	if len(bels) == 0 {
		t.Log("WARNING: No BEL signals detected.")
		t.Log("Dumping control chars found:")
		dumpTail(t, "output", output, 300)
	}

	// The test passes regardless — this is a PoC to observe behavior.
}

// drainUntilIdle reads from the shared dataCh until no data arrives for idleTimeout
// (after at least some data is received), or until the hard deadline expires.
// Returns the accumulated bytes and BEL byte positions.
func drainUntilIdle(t *testing.T, dataCh <-chan []byte, errCh <-chan error, hardTimeout, idleTimeout time.Duration) ([]byte, []int) {
	t.Helper()

	deadline := time.After(hardTimeout)
	var buf bytes.Buffer
	var belPositions []int
	receivedAny := false
	idleTimer := time.NewTimer(idleTimeout)
	defer idleTimer.Stop()

	for {
		select {
		case data := <-dataCh:
			receivedAny = true
			for i, b := range data {
				if b == 0x07 {
					belPositions = append(belPositions, buf.Len()+i)
				}
			}
			buf.Write(data)
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(idleTimeout)
		case err := <-errCh:
			if err == io.EOF {
				t.Log("PTY EOF reached")
			} else {
				t.Logf("PTY read error: %v", err)
			}
			return buf.Bytes(), belPositions
		case <-idleTimer.C:
			if receivedAny {
				t.Logf("Idle timeout reached (no new data for %v) — stopping read", idleTimeout)
				return buf.Bytes(), belPositions
			}
			idleTimer.Reset(idleTimeout)
		case <-deadline:
			t.Log("Hard deadline reached — stopping read")
			return buf.Bytes(), belPositions
		}
	}
}

// logBELContext prints surrounding bytes around each BEL position for analysis.
func logBELContext(t *testing.T, data []byte, positions []int) {
	t.Helper()
	for _, pos := range positions {
		start := pos - 30
		if start < 0 {
			start = 0
		}
		end := pos + 30
		if end > len(data) {
			end = len(data)
		}
		t.Logf("  BEL at byte %d — context hex: %x", pos, data[start:end])
		t.Logf("  BEL at byte %d — context str: %q", pos, string(data[start:end]))
	}
}

// dumpTail prints the last N bytes of data in hex and string format.
func dumpTail(t *testing.T, label string, data []byte, n int) {
	t.Helper()
	start := len(data) - n
	if start < 0 {
		start = 0
	}
	tail := data[start:]
	t.Logf("  [%s] last %d bytes hex: %x", label, len(tail), tail)
	t.Logf("  [%s] last %d bytes str: %q", label, len(tail), string(tail))

	// Also count all control chars.
	controlCounts := make(map[byte]int)
	for _, b := range data {
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' {
			controlCounts[b]++
		}
	}
	if len(controlCounts) > 0 {
		t.Logf("  [%s] control chars found: %v", label, formatControlCounts(controlCounts))
	}
}

func formatControlCounts(counts map[byte]int) string {
	var parts []string
	names := map[byte]string{
		0x01: "SOH", 0x02: "STX", 0x03: "ETX", 0x04: "EOT",
		0x05: "ENQ", 0x06: "ACK", 0x07: "BEL", 0x08: "BS",
		0x0B: "VT", 0x0C: "FF", 0x0E: "SO", 0x0F: "SI",
		0x1B: "ESC", 0x7F: "DEL",
	}
	for b, count := range counts {
		name := names[b]
		if name == "" {
			name = fmt.Sprintf("0x%02x", b)
		}
		parts = append(parts, fmt.Sprintf("%s=%d", name, count))
	}
	return strings.Join(parts, ", ")
}

// stripESC removes ANSI escape sequences for human-readable logging.
func stripESC(s string) string {
	var out strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == 0x1B {
			// Skip ESC sequence.
			i++
			if i < len(s) && s[i] == '[' {
				i++
				for i < len(s) && !((s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z')) {
					i++
				}
				if i < len(s) {
					i++ // skip the final letter
				}
			}
		} else if s[i] == '\r' {
			i++
		} else {
			out.WriteByte(s[i])
			i++
		}
	}
	return out.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
