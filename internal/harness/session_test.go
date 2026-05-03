package harness

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func waitForSessionState(t *testing.T, events <-chan SessionEvent, want SessionState) SessionEvent {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case evt := <-events:
			if evt.State == want {
				return evt
			}
		case <-timeout:
			t.Fatalf("timed out waiting for session state %v", want)
			return SessionEvent{}
		}
	}
}

func TestSessionManager_StartSession(t *testing.T) {
	client := NewClient("test-model", nil)

	events := make(chan SessionEvent, 100)
	sm := NewSessionManager(client, func(evt SessionEvent) {
		events <- evt
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so claude doesn't actually run

	var buf bytes.Buffer
	id := sm.StartSession(ctx, "Test", "hello", "", &buf)

	if id == "" {
		t.Fatal("expected non-empty session ID")
	}

	// Wait for terminal state (Failed due to cancelled ctx)
	waitForSessionState(t, events, SessionFailed)
}

func TestSessionManager_Sessions(t *testing.T) {
	client := NewClient("test-model", nil)

	events := make(chan SessionEvent, 100)
	sm := NewSessionManager(client, func(evt SessionEvent) {
		events <- evt
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var buf bytes.Buffer
	sm.StartSession(ctx, "A", "p1", "", &buf)
	sm.StartSession(ctx, "B", "p2", "", &buf)

	// Wait for both to finish
	waitForSessionState(t, events, SessionFailed)
	waitForSessionState(t, events, SessionFailed)

	sessions := sm.Sessions()
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	if sessions[0].Name != "A" || sessions[1].Name != "B" {
		t.Errorf("sessions out of order: %s, %s", sessions[0].Name, sessions[1].Name)
	}
}

func TestSessionManager_SetNotify(t *testing.T) {
	client := NewClient("test-model", nil)
	sm := NewSessionManager(client, nil)

	events := make(chan SessionEvent, 100)
	var called atomic.Bool
	sm.SetNotify(func(evt SessionEvent) {
		called.Store(true)
		events <- evt
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var buf bytes.Buffer
	sm.StartSession(ctx, "X", "p", "", &buf)

	waitForSessionState(t, events, SessionFailed)

	if !called.Load() {
		t.Error("expected notify to be called after SetNotify")
	}
}
