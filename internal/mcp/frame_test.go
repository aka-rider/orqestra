package mcp

import (
	"bytes"
	"strings"
	"testing"
)

// TestFrameRoundTrip_OverOneMB verifies that a frame just over the old 1 MB
// sanity limit round-trips through writeFrame/readFrame. Before the fix,
// readFrame rejected any frame over 1<<20 bytes with "frame too large" even
// though a legitimate SubmitReport payload (e.g. a large markdown deliverable)
// can exceed 1 MB. The cap is now maxFrameBytes (2 MB), matching
// harness.maxJSONLLineBytes.
func TestFrameRoundTrip_OverOneMB(t *testing.T) {
	// INV-MCP-FRAMECAP: a >1MB, <=maxFrameBytes payload must round-trip.
	const overOneMB = (1 << 20) + (256 * 1024) // 1.25 MB
	payload := []byte(strings.Repeat("r", overOneMB))

	var buf bytes.Buffer
	if err := writeFrame(&buf, payload); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}

	got, err := readFrame(&buf)
	if err != nil {
		t.Fatalf("readFrame on a %d-byte frame (over the old 1MB cap): %v", len(payload), err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("round-tripped frame does not match: got %d bytes, want %d bytes", len(got), len(payload))
	}
}

// TestFrameRoundTrip_OverMaxRejected verifies the cap still rejects frames
// beyond maxFrameBytes — raising the ceiling must not remove it entirely.
func TestFrameRoundTrip_OverMaxRejected(t *testing.T) {
	var buf bytes.Buffer
	// Write a frame header claiming a size one byte over maxFrameBytes,
	// without materializing that much data (readFrame must reject before
	// attempting to read the body).
	oversized := uint32(maxFrameBytes + 1)
	lenBuf := []byte{byte(oversized >> 24), byte(oversized >> 16), byte(oversized >> 8), byte(oversized)}
	buf.Write(lenBuf)

	if _, err := readFrame(&buf); err == nil {
		t.Fatal("expected readFrame to reject a frame larger than maxFrameBytes")
	}
}
