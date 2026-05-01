package harness

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionManager_StartSession(t *testing.T) {
	client := NewClient("test-model", nil)

	var mu sync.Mutex
	var events []SessionEvent

	sm := NewSessionManager(client, func(evt SessionEvent) {
		mu.Lock()
		events = append(events, evt)
		mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so claude doesn't actually run

	var buf bytes.Buffer
	id := sm.StartSession(ctx, "Test", "hello", "", &buf)

	// Wait for session to complete (it should fail fast due to cancelled ctx)
	time.Sleep(200 * time.Millisecond)

	if id == "" {
		t.Fatal("expected non-empty session ID")
	}

	mu.Lock()
	defer mu.Unlock()

	if len(events) < 2 {
		t.Fatalf("expected at least 2 events (pending, running/failed), got %d", len(events))
	}

	if events[0].State != SessionPending {
		t.Errorf("first event should be Pending, got %v", events[0].State)
	}

	// Should eventually reach Failed due to cancelled context
	lastEvt := events[len(events)-1]
	if lastEvt.State != SessionFailed {
		t.Errorf("last event should be Failed (cancelled ctx), got %v", lastEvt.State)
	}
}

func TestSessionManager_Sessions(t *testing.T) {
	client := NewClient("test-model", nil)
	sm := NewSessionManager(client, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var buf bytes.Buffer
	sm.StartSession(ctx, "A", "p1", "", &buf)
	sm.StartSession(ctx, "B", "p2", "", &buf)

	time.Sleep(100 * time.Millisecond)

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

	var called atomic.Bool
	sm.SetNotify(func(evt SessionEvent) {
		called.Store(true)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var buf bytes.Buffer
	sm.StartSession(ctx, "X", "p", "", &buf)

	time.Sleep(100 * time.Millisecond)

	if !called.Load() {
		t.Error("expected notify to be called after SetNotify")
	}
}
