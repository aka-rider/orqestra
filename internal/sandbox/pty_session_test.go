//go:build integration

package sandbox

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	dockerclient "github.com/docker/docker/client"
	"github.com/stretchr/testify/require"
)

// setupPTYTestContainer creates a temporary alpine container for PTY tests.
func setupPTYTestContainer(t *testing.T) (*dockerclient.Client, string) {
	t.Helper()

	cli, err := newDockerClient()
	require.NoError(t, err, "creating docker client")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Ensure alpine:latest is available.
	_, err = cli.ImagePull(ctx, "alpine:latest", image.PullOptions{})
	if err != nil {
		// Try to use it anyway — it might already be present.
		_, _, err2 := cli.ImageInspectWithRaw(ctx, "alpine:latest")
		require.NoError(t, err2, "alpine:latest not available and pull failed: %v", err)
	} else {
		// Drain the pull reader.
		r, _ := cli.ImagePull(ctx, "alpine:latest", image.PullOptions{})
		if r != nil {
			_, _ = io.Copy(io.Discard, r)
			r.Close()
		}
	}

	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image: "alpine:latest",
		Cmd:   []string{"sleep", "300"},
		Tty:   false,
	}, nil, nil, nil, "")
	require.NoError(t, err, "creating test container")

	err = cli.ContainerStart(ctx, resp.ID, container.StartOptions{})
	require.NoError(t, err, "starting test container")

	t.Cleanup(func() {
		_ = cli.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
	})

	return cli, resp.ID
}

// readUntil reads from the PTY session until the output contains the expected string or timeout.
func readUntil(t *testing.T, ps *PTYSession, expected string, timeout time.Duration) string {
	t.Helper()
	deadline := time.After(timeout)
	var buf strings.Builder
	tmp := make([]byte, 4096)

	// Use a goroutine to perform non-blocking reads with short deadlines.
	dataCh := make(chan []byte, 100)
	errCh := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)

	go func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			n, err := ps.Read(tmp)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, tmp[:n])
				select {
				case dataCh <- cp:
				case <-done:
					return
				}
			}
			if err != nil {
				select {
				case errCh <- err:
				case <-done:
				}
				return
			}
		}
	}()

	for {
		select {
		case data := <-dataCh:
			buf.Write(data)
			if strings.Contains(buf.String(), expected) {
				return buf.String()
			}
		case <-errCh:
			return buf.String()
		case <-deadline:
			t.Logf("readUntil timeout; got so far: %q", buf.String())
			return buf.String()
		}
	}
}

// drainUntilEOF reads the PTY session until EOF.
func drainUntilEOF(t *testing.T, ps *PTYSession, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var buf strings.Builder
	tmp := make([]byte, 4096)
	for time.Now().Before(deadline) {
		n, err := ps.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if err != nil {
			break
		}
	}
	return buf.String()
}

func TestDockerPTYSession_BasicIO(t *testing.T) {
	cli, containerID := setupPTYTestContainer(t)
	ctx := context.Background()

	ps := NewPTYSession("test-basic", "basic-io", cli)
	err := ps.Start(ctx, containerID, []string{"echo", "hello"}, nil, 80, 24)
	require.NoError(t, err)
	defer ps.Close()

	output := drainUntilEOF(t, ps, 5*time.Second)
	require.Contains(t, output, "hello")

	// Wait for exit monitor.
	time.Sleep(200 * time.Millisecond)
	require.Equal(t, 0, ps.ExitCode())
}

func TestDockerPTYSession_Bidirectional(t *testing.T) {
	cli, containerID := setupPTYTestContainer(t)
	ctx := context.Background()

	ps := NewPTYSession("test-bidir", "bidirectional", cli)
	err := ps.Start(ctx, containerID, []string{"cat"}, nil, 80, 24)
	require.NoError(t, err)
	defer ps.Close()

	// Give cat time to start.
	time.Sleep(100 * time.Millisecond)

	_, err = ps.Write([]byte("foo\n"))
	require.NoError(t, err)

	output := readUntil(t, ps, "foo", 5*time.Second)
	require.Contains(t, output, "foo")
}

func TestDockerPTYSession_Interactive(t *testing.T) {
	cli, containerID := setupPTYTestContainer(t)
	ctx := context.Background()

	ps := NewPTYSession("test-interact", "interactive", cli)
	err := ps.Start(ctx, containerID, []string{"sh", "-c", "read x; echo got $x"}, nil, 80, 24)
	require.NoError(t, err)
	defer ps.Close()

	// Give sh time to start.
	time.Sleep(100 * time.Millisecond)

	_, err = ps.Write([]byte("bar\n"))
	require.NoError(t, err)

	output := readUntil(t, ps, "got bar", 5*time.Second)
	require.Contains(t, output, "got bar")
}

func TestDockerPTYSession_ExitCode(t *testing.T) {
	cli, containerID := setupPTYTestContainer(t)
	ctx := context.Background()

	ps := NewPTYSession("test-exit", "exit-code", cli)
	err := ps.Start(ctx, containerID, []string{"sh", "-c", "exit 42"}, nil, 80, 24)
	require.NoError(t, err)
	defer ps.Close()

	// Drain until EOF to let exit monitor run.
	drainUntilEOF(t, ps, 5*time.Second)
	time.Sleep(200 * time.Millisecond)

	require.Equal(t, 42, ps.ExitCode())
	require.Equal(t, SessionFailed, ps.State())
}

func TestDockerPTYSession_SigintClose(t *testing.T) {
	cli, containerID := setupPTYTestContainer(t)
	ctx := context.Background()

	ps := NewPTYSession("test-sigint", "sigint-close", cli)
	err := ps.Start(ctx, containerID, []string{"sleep", "60"}, nil, 80, 24)
	require.NoError(t, err)

	// Give sleep time to start.
	time.Sleep(200 * time.Millisecond)

	err = ps.Close()
	require.NoError(t, err)

	// Verify process exited within 5s.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ps.State() != SessionRunning {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.NotEqual(t, SessionRunning, ps.State())
}

func TestDockerPTYSession_Resize(t *testing.T) {
	cli, containerID := setupPTYTestContainer(t)
	ctx := context.Background()

	ps := NewPTYSession("test-resize", "resize", cli)
	err := ps.Start(ctx, containerID, []string{"sh"}, nil, 80, 24)
	require.NoError(t, err)
	defer ps.Close()

	// Give sh time to start.
	time.Sleep(200 * time.Millisecond)

	// Resize.
	err = ps.Resize(120, 40)
	require.NoError(t, err)

	// Give time for resize to take effect.
	time.Sleep(200 * time.Millisecond)

	// Ask for terminal size — stty is available in Alpine.
	_, err = ps.Write([]byte("stty size\n"))
	require.NoError(t, err)

	output := readUntil(t, ps, "40 120", 5*time.Second)
	require.Contains(t, output, "40 120")
}

func TestDockerPTYSession_DoubleClose(t *testing.T) {
	cli, containerID := setupPTYTestContainer(t)
	ctx := context.Background()

	ps := NewPTYSession("test-double", "double-close", cli)
	err := ps.Start(ctx, containerID, []string{"echo", "x"}, nil, 80, 24)
	require.NoError(t, err)

	drainUntilEOF(t, ps, 5*time.Second)

	err = ps.Close()
	require.NoError(t, err)

	err = ps.Close()
	require.NoError(t, err)
}
