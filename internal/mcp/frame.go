package mcp

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

// maxFrameBytes bounds a single length-prefixed bridge frame (question,
// answer, or report envelope). Kept in sync with harness.maxJSONLLineBytes
// (internal/harness/stream_event.go) — a SubmitReport payload can legitimately
// approach the same ceiling as a single stream-JSON line, so the two must not
// diverge. Defined here (not imported from harness) so the mcp package has no
// dependency on harness for a sanity constant.
const maxFrameBytes = 2 << 20 // 2 MB — matches harness.maxJSONLLineBytes

// writeFrame writes a length-prefixed binary frame.
func writeFrame(w io.Writer, data []byte) error {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(data)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

// readFrame reads a length-prefixed binary frame.
func readFrame(r io.Reader) ([]byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n > maxFrameBytes {
		return nil, fmt.Errorf("frame too large: %d bytes", n)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}
	return data, nil
}

// readFrameDeadline reads one length-prefixed frame off conn, bounded by
// timeout (A7 — "readFrame has no deadline, a hung peer leaks past Run's
// return"): a connected-but-silent peer (or one that sends a partial frame
// and stops) now fails with a deadline-exceeded error instead of blocking
// the caller's goroutine forever. timeout<=0 disables the bound (readFrame
// itself, used directly against plain io.Reader values such as bytes.Buffer
// in frame_test.go, has no deadline concept at all).
func readFrameDeadline(conn net.Conn, timeout time.Duration) ([]byte, error) {
	if timeout <= 0 {
		return readFrame(conn)
	}
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, fmt.Errorf("set read deadline: %w", err)
	}
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }() // fire-and-forget: best-effort reset — conn is handled or closed regardless of whether this succeeds
	return readFrame(conn)
}

// writeFrameDeadline writes one length-prefixed frame to conn, bounded by
// timeout — the write-side counterpart of readFrameDeadline: a peer that
// stops reading (e.g. it crashed after sending its request) cannot block
// the caller's goroutine forever on the response write either.
func writeFrameDeadline(conn net.Conn, data []byte, timeout time.Duration) error {
	if timeout <= 0 {
		return writeFrame(conn, data)
	}
	if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	defer func() { _ = conn.SetWriteDeadline(time.Time{}) }() // fire-and-forget: best-effort reset — conn is handled or closed regardless of whether this succeeds
	return writeFrame(conn, data)
}
