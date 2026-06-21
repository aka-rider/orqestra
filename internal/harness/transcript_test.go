package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// transcriptFixture returns the absolute path to testdata/transcripts/<scenario>/<file>
// by walking up to the module root (go.mod). Fails the test if not found.
func transcriptFixture(t *testing.T, scenario, file string) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			p := filepath.Join(dir, "testdata", "transcripts", scenario, file)
			if _, err := os.Stat(p); err != nil {
				t.Fatalf("transcript fixture missing: %s", p)
			}
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// TestExtractPlanFilePath_RealFixture runs ExtractPlanFilePath against a
// real minimized+sanitized session JSONL captured from a live claude CLI run.
// This is a Tier-A hermetic test: no claude binary, no network.
func TestExtractPlanFilePath_RealFixture(t *testing.T) {
	// INV-P1-PLANSRC: plan file path extracted from real session JSONL attachment
	fixturePath := transcriptFixture(t, "plan-mode", "session.jsonl")

	got, err := ExtractPlanFilePath(fixturePath)
	if err != nil {
		t.Fatalf("ExtractPlanFilePath on real fixture: %v", err)
	}

	// The fixture contains a sanitized path — verify it has the expected shape.
	const wantSuffix = "investigate-the-latest-orqestra-floating-whale.md"
	if !strings.HasSuffix(got, wantSuffix) {
		t.Errorf("extracted path %q does not end with %q", got, wantSuffix)
	}
	if !strings.Contains(got, ".claude/plans/") {
		t.Errorf("extracted path %q should be under .claude/plans/", got)
	}
}

// TestExtractPlanFilePath_NoPlanAttachment_RealFixture runs ExtractPlanFilePath
// against a real session JSONL that contains no plan_mode attachment.
func TestExtractPlanFilePath_NoPlanAttachment_RealFixture(t *testing.T) {
	// INV-P1-PLANSRC: missing plan_mode attachment → error, not silent empty result
	fixturePath := transcriptFixture(t, "no-plan", "session.jsonl")

	_, err := ExtractPlanFilePath(fixturePath)
	if err == nil {
		t.Fatal("expected error for session with no plan_mode attachment")
	}
	if !strings.Contains(err.Error(), "no plan_mode attachment found") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestParseSessionLogStream_RealFixture runs ParseSessionLogStream against a
// real minimized session JSONL fixture with assistant and user messages.
func TestParseSessionLogStream_RealFixture(t *testing.T) {
	// INV-P4-STREAM: real session JSONL parsed into structured events without error
	fixturePath := transcriptFixture(t, "plan-mode", "session.jsonl")

	f, err := os.Open(fixturePath)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	events, err := ParseSessionLogStream(f)
	if err != nil {
		t.Fatalf("ParseSessionLogStream on real fixture: %v", err)
	}

	// The fixture has one assistant text message — expect at least one EventChunk.
	var hasChunk bool
	for _, ev := range events {
		if ev.Kind == EventChunk && ev.Text != "" {
			hasChunk = true
			break
		}
	}
	if !hasChunk {
		t.Errorf("expected at least one EventChunk from real fixture, got %d events", len(events))
	}
}

// TestParseStream_WorkerComplete_RealFixture runs the internal parseStream
// against a captured worker stream fixture and verifies it extracts the
// session_id and result from the real stream format.
func TestParseStream_WorkerComplete_RealFixture(t *testing.T) {
	// INV-H2-SESSIONID: session_id extracted from real worker stream is non-empty
	fixturePath := transcriptFixture(t, "worker-complete", "stream.jsonl")

	f, err := os.Open(fixturePath)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	result, isError, usage, sessionID, _, parseErr := parseStream(f, nil)
	if parseErr != nil {
		t.Fatalf("parseStream on real fixture: %v", parseErr)
	}

	if isError {
		t.Error("worker-complete fixture should not be an error result")
	}
	if result == "" {
		t.Error("expected non-empty result from worker-complete fixture")
	}
	if sessionID == "" {
		t.Error("expected non-empty session_id from worker-complete fixture")
	}
	// The fixture has minimal token counts; just verify they're non-negative.
	if usage.Input < 0 || usage.Output < 0 {
		t.Errorf("unexpected negative token usage: input=%d output=%d", usage.Input, usage.Output)
	}
}
