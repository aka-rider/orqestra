//go:build darwin

package mux

import (
	"context"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xiii/orqestra/internal/agent"
)

func TestTab_ReadLoop_ActiveTab(t *testing.T) {
	// Spawn a process that writes known output.
	cmd := exec.Command("echo", "hello from pty")
	npty, err := agent.StartNativePTY(cmd, 80, 24)
	require.NoError(t, err)
	defer npty.Close()

	tab := NewTab("test", 0, npty)

	// Create a buffer writer to capture output.
	var buf safeBuffer
	var activeIdx atomic.Int32
	activeIdx.Store(0) // This tab is active.

	go tab.ReadLoop(&buf, &activeIdx)

	// Wait for process to exit.
	select {
	case <-tab.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for tab to finish")
	}

	assert.Equal(t, TabDone, tab.State)
	assert.Equal(t, 0, tab.ExitCode)
	assert.Contains(t, buf.String(), "hello from pty")
}

func TestTab_ReadLoop_InactiveTab_Discards(t *testing.T) {
	cmd := exec.Command("echo", "invisible output")
	npty, err := agent.StartNativePTY(cmd, 80, 24)
	require.NoError(t, err)
	defer npty.Close()

	tab := NewTab("bg", 0, npty)

	var buf safeBuffer
	var activeIdx atomic.Int32
	activeIdx.Store(1) // Different tab is active — this tab's output should be discarded.

	go tab.ReadLoop(&buf, &activeIdx)

	select {
	case <-tab.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for tab to finish")
	}

	assert.Equal(t, TabDone, tab.State)
	assert.Empty(t, buf.String(), "inactive tab output should be discarded")
}

func TestTab_BEL_Detection(t *testing.T) {
	// Use /bin/sh -c to send a BEL character.
	cmd := exec.Command("/bin/sh", "-c", "printf 'attention\\007needed'")
	npty, err := agent.StartNativePTY(cmd, 80, 24)
	require.NoError(t, err)
	defer npty.Close()

	tab := NewTab("bel", 0, npty)

	var buf safeBuffer
	var activeIdx atomic.Int32
	activeIdx.Store(0)

	go tab.ReadLoop(&buf, &activeIdx)

	select {
	case <-tab.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for tab to finish")
	}

	assert.True(t, tab.NeedsAttention(), "BEL should set attention flag")
	tab.ClearAttention()
	assert.False(t, tab.NeedsAttention(), "attention should be cleared")
}

func TestMux_AddTab(t *testing.T) {
	m := New(Config{})
	// We can't fully test Run without a tty, but we can test tab management.
	// Mock the tty with a pipe.
	cmd := exec.Command("sleep", "0.1")
	npty, err := agent.StartNativePTY(cmd, 80, 24)
	require.NoError(t, err)
	defer npty.Close()

	// Create a pipe to act as our "tty" for the tab read loop.
	r, w, err := pipePair()
	require.NoError(t, err)
	defer r.Close()
	defer w.Close()
	setReadyForTest(m, w)

	idx := m.AddTab("test-tab", npty)
	assert.Equal(t, 0, idx)
	assert.Equal(t, 1, m.TabCount())

	tabs := m.Tabs()
	assert.Equal(t, "test-tab", tabs[0].Name)
}

func TestMux_Shutdown(t *testing.T) {
	m := New(Config{})

	// Start a long-running process.
	cmd := exec.Command("sleep", "60")
	npty, err := agent.StartNativePTY(cmd, 80, 24)
	require.NoError(t, err)

	r, w, err := pipePair()
	require.NoError(t, err)
	defer r.Close()
	defer w.Close()
	setReadyForTest(m, w)

	_ = m.AddTab("long", npty)

	// Shutdown should kill the process quickly.
	done := make(chan struct{})
	go func() {
		m.Shutdown()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown took too long")
	}
}

func TestMux_OnTabExited_Callback(t *testing.T) {
	var gotEvent TabExitedEvent
	var called atomic.Bool

	m := New(Config{
		OnTabExited: func(event TabExitedEvent) {
			gotEvent = event
			called.Store(true)
		},
	})

	cmd := exec.Command("true") // exits immediately with 0
	npty, err := agent.StartNativePTY(cmd, 80, 24)
	require.NoError(t, err)

	r, w, err := pipePair()
	require.NoError(t, err)
	defer r.Close()
	defer w.Close()
	setReadyForTest(m, w)

	idx := m.AddTab("callback-test", npty)

	// Wait for tab to exit.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	select {
	case <-m.tabs[idx].Done():
	case <-ctx.Done():
		t.Fatal("timeout waiting for tab exit")
	}

	// Give the goroutine a moment to call the callback.
	time.Sleep(50 * time.Millisecond)

	assert.True(t, called.Load())
	assert.Equal(t, 0, gotEvent.Index)
	assert.Equal(t, 0, gotEvent.ExitCode)
}

// safeBuffer is a thread-safe buffer for test use.
type safeBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	b.buf = append(b.buf, p...)
	b.mu.Unlock()
	return len(p), nil
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
