package harness

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

// SessionState represents the lifecycle state of a harness session.
type SessionState int

const (
	SessionPending SessionState = iota // Created but not started
	SessionRunning                     // Subprocess is active
	SessionDone                        // Completed successfully
	SessionFailed                      // Completed with error
)

func (s SessionState) String() string {
	switch s {
	case SessionPending:
		return "pending"
	case SessionRunning:
		return "running"
	case SessionDone:
		return "done"
	case SessionFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// Session represents a single harness subprocess with tracked lifecycle.
type Session struct {
	ID        string
	Name      string
	State     SessionState
	StartedAt time.Time
	DoneAt    time.Time
	Err       error
	Response  *Response

	// Sandbox fields (populated when running in a sandboxed environment).
	Sandboxed   bool   // true if this session runs inside a Docker sandbox
	SandboxID   string // sandbox identifier for display
	ContainerID string // short container hash for display
	SandboxInfo string // human-readable sandbox state (e.g. "running | 2m14s")

	client *Client
	prompt string
	system string
}

// SessionEvent is emitted when a session's state changes.
type SessionEvent struct {
	SessionID    string
	Name         string
	State        SessionState
	Err          error
	SandboxState string // optional: sandbox lifecycle state for TUI display
}

// SessionManager manages multiple concurrent harness sessions and notifies
// observers of state changes.
type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	order    []string // insertion order
	client   *Client
	notify   func(SessionEvent)
	nextID   int
}

// NewSessionManager creates a manager that uses the given client config
// and emits events via the notify callback.
func NewSessionManager(client *Client, notify func(SessionEvent)) *SessionManager {
	return &SessionManager{
		sessions: make(map[string]*Session),
		client:   client,
		notify:   notify,
	}
}

// StartSession creates and immediately starts a new harness session.
// The session streams output to the provided writer.
// Returns the session ID for tracking.
func (sm *SessionManager) StartSession(ctx context.Context, name, prompt, systemPrompt string, stdout io.Writer) string {
	sm.mu.Lock()
	sm.nextID++
	id := fmt.Sprintf("session-%d", sm.nextID)
	sess := &Session{
		ID:     id,
		Name:   name,
		State:  SessionPending,
		client: sm.client,
		prompt: prompt,
		system: systemPrompt,
	}
	sm.sessions[id] = sess
	sm.order = append(sm.order, id)
	sm.mu.Unlock()

	sm.emit(SessionEvent{SessionID: id, Name: name, State: SessionPending})

	// Run in background
	go sm.run(ctx, sess, stdout)

	return id
}

func (sm *SessionManager) run(ctx context.Context, sess *Session, stdout io.Writer) {
	sm.mu.Lock()
	sess.State = SessionRunning
	sess.StartedAt = time.Now()
	sm.mu.Unlock()

	sm.emit(SessionEvent{SessionID: sess.ID, Name: sess.Name, State: SessionRunning})

	resp, err := sess.client.RunStreaming(ctx, sess.prompt, sess.system, stdout)

	sm.mu.Lock()
	sess.DoneAt = time.Now()
	sess.Response = resp
	if err != nil {
		sess.State = SessionFailed
		sess.Err = err
	} else {
		sess.State = SessionDone
	}
	sm.mu.Unlock()

	sm.emit(SessionEvent{SessionID: sess.ID, Name: sess.Name, State: sess.State, Err: err})
}

// GetSession returns a snapshot of a session by ID.
func (sm *SessionManager) GetSession(id string) (Session, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	s, ok := sm.sessions[id]
	if !ok {
		return Session{}, false
	}
	copy := *s
	if s.Response != nil {
		r := *s.Response
		copy.Response = &r
	}
	return copy, true
}

// Sessions returns all sessions in creation order.
func (sm *SessionManager) Sessions() []Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	result := make([]Session, 0, len(sm.order))
	for _, id := range sm.order {
		s := sm.sessions[id]
		copy := *s
		if s.Response != nil {
			r := *s.Response
			copy.Response = &r
		}
		result = append(result, copy)
	}
	return result
}

// Response returns the response of a completed session.
func (sm *SessionManager) Response(id string) (*Response, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	s, ok := sm.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session %q not found", id)
	}
	if s.State == SessionPending || s.State == SessionRunning {
		return nil, fmt.Errorf("session %q still running", id)
	}
	if s.Err != nil {
		return nil, s.Err
	}
	if s.Response == nil {
		return nil, nil
	}
	r := *s.Response
	return &r, nil
}

func (sm *SessionManager) emit(evt SessionEvent) {
	if sm.notify != nil {
		sm.notify(evt)
	}
}

// SetNotify sets or replaces the event notification callback.
func (sm *SessionManager) SetNotify(fn func(SessionEvent)) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.notify = fn
}
