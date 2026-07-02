package mcp

import (
	"encoding/binary"
	"fmt"
	"io"
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
