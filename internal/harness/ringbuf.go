package harness

import "sync"

// RingBuffer is a fixed-size circular buffer of StreamEvents.
// It retains the newest N events, discarding the oldest on overflow.
type RingBuffer struct {
	mu    sync.Mutex
	buf   []StreamEvent
	pos   int
	count int
	cap   int
}

// NewRingBuffer creates a ring buffer with the given capacity.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 1000
	}
	return &RingBuffer{
		buf: make([]StreamEvent, capacity),
		cap: capacity,
	}
}

// Push appends an event to the buffer, overwriting the oldest if full.
func (r *RingBuffer) Push(event StreamEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.pos] = event
	r.pos = (r.pos + 1) % r.cap
	if r.count < r.cap {
		r.count++
	}
}

// Events returns all stored events in order (oldest first).
func (r *RingBuffer) Events() []StreamEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.count == 0 {
		return nil
	}
	result := make([]StreamEvent, r.count)
	if r.count < r.cap {
		copy(result, r.buf[:r.count])
	} else {
		// Buffer is full — read from pos (oldest) wrapping around.
		n := copy(result, r.buf[r.pos:])
		copy(result[n:], r.buf[:r.pos])
	}
	return result
}

// Len returns the number of events currently stored.
func (r *RingBuffer) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}
