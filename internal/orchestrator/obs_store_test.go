package orchestrator

import (
	"testing"

	"github.com/xiii/orqestra/internal/harness"
)

// TestObsStore_Stream_ToolResult verifies that an EventToolResult with
// IsError:true produces an EntryToolResult{ToolErr:true} on streamCh and in
// the ring, and that IsError:false produces ToolErr:false.
func TestObsStore_Stream_ToolResult(t *testing.T) {
	obs := NewObsStore()
	obs.AgentStarted("researcher", AgentMeta{})

	// IsError:true
	obs.Stream("researcher", harness.Event{
		Kind:    harness.EventToolResult,
		IsError: true,
	})

	var got StreamEntry
	select {
	case got = <-obs.StreamCh():
	default:
		t.Fatal("expected entry on streamCh")
	}
	if got.Kind != EntryToolResult {
		t.Errorf("got Kind=%v, want EntryToolResult", got.Kind)
	}
	if !got.ToolErr {
		t.Errorf("got ToolErr=false, want true")
	}

	// IsError:false
	obs.Stream("researcher", harness.Event{
		Kind:    harness.EventToolResult,
		IsError: false,
	})
	select {
	case got = <-obs.StreamCh():
	default:
		t.Fatal("expected entry on streamCh for IsError:false")
	}
	if got.Kind != EntryToolResult {
		t.Errorf("got Kind=%v, want EntryToolResult", got.Kind)
	}
	if got.ToolErr {
		t.Errorf("got ToolErr=true, want false")
	}
}
