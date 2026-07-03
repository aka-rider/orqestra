package orchestrator

// INV: a silence-guard escalation during architect revision must be treated
// as non-fatal — fall back to the previous plan and keep going — while a
// genuine outer-context cancellation (e.g. TUI-attributed user cancel) must
// propagate the attributed cause via context.Cause, not the bare ctx.Err().

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/xiii/orqestra/internal/harness"
)

// recordingArtifactSink records every WriteBestEffort/Write call by name, so
// tests can inspect the meta JSON a step persisted (J12/WP8: proving the meta
// artifact's Status field, not just the step's return value).
type recordingArtifactSink struct {
	writes map[string][]byte
}

func newRecordingArtifactSink() *recordingArtifactSink {
	return &recordingArtifactSink{writes: make(map[string][]byte)}
}

func (r *recordingArtifactSink) Write(name string, data []byte) error {
	r.writes[name] = data
	return nil
}

func (r *recordingArtifactSink) WriteBestEffort(name string, data []byte) {
	r.writes[name] = data
}

// sequencedExecutor returns each configured (RunResult, error) pair in order
// across successive Run calls — used to drive runRound's critic-then-revision
// sequencing deterministically without a real subprocess.
type sequencedExecutor struct {
	calls   int
	results []harness.RunResult
	errs    []error
}

func (s *sequencedExecutor) Run(_ context.Context, _ harness.ProcessSpec, _ <-chan harness.Message, _ harness.Sink) (harness.RunResult, error) {
	i := s.calls
	if i >= len(s.results) {
		i = len(s.results) - 1
	}
	s.calls++
	return s.results[i], s.errs[i]
}

func TestDeliberateStep_RunRound_SilenceEscalationFallsBackNonFatal(t *testing.T) {
	const prevPlan = "# Plan\n\nOriginal plan content."
	exec := &sequencedExecutor{
		results: []harness.RunResult{
			{Output: "# Critic Report\n\nNo blockers.", SessionID: "critic-sid"},
			{SessionID: "arch-sid"},
		},
		errs: []error{nil, ErrSilenceEscalated},
	}

	step := &DeliberateStep{
		ArchSpec:   harness.ProcessSpec{AgentID: "architect"},
		CriticSpec: harness.ProcessSpec{AgentID: "critic"},
		HasCritic:  true,
	}

	artifacts := newRecordingArtifactSink()
	sc := StepContext{
		Exec:      exec,
		Obs:       NewObsStore(),
		Artifacts: artifacts,
		Log:       slog.Default(),
	}

	revised, sessionID, _, err := step.runRound(context.Background(), prevPlan, "orig-sid", "", 0, "do the thing", sc)
	if err != nil {
		t.Fatalf("expected non-fatal fallback (nil error), got %v", err)
	}
	if revised != prevPlan {
		t.Errorf("expected fallback to previous plan, got %q", revised)
	}
	if sessionID != "orig-sid" {
		t.Errorf("expected session ID to stay at the last known-good plan's session, got %q", sessionID)
	}

	// J12/WP8: a chat-only fallback (no plan rewrite) must NOT be recorded as a
	// clean "done" in the meta artifact — that would show a successful revision
	// that never happened.
	data, ok := artifacts.writes["architect_critic_revision_meta_round1.json"]
	if !ok {
		t.Fatal("expected architect_critic_revision_meta_round1.json to be written")
	}
	var meta StepMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("failed to unmarshal meta artifact: %v", err)
	}
	if meta.Status != "fallback" {
		t.Errorf("J12: meta Status = %q, want %q (a chat-only fallback revision must not claim \"done\")", meta.Status, "fallback")
	}
}

func TestDeliberateStep_RunRound_UserCancelIsFatalWithAttributedCause(t *testing.T) {
	const prevPlan = "# Plan\n\nOriginal plan content."
	exec := &sequencedExecutor{
		results: []harness.RunResult{
			{Output: "# Critic Report\n\nNo blockers.", SessionID: "critic-sid"},
			{},
		},
		errs: []error{nil, context.Canceled},
	}

	step := &DeliberateStep{
		ArchSpec:   harness.ProcessSpec{AgentID: "architect"},
		CriticSpec: harness.ProcessSpec{AgentID: "critic"},
		HasCritic:  true,
	}

	sc := StepContext{
		Exec:      exec,
		Obs:       NewObsStore(),
		Artifacts: NoopArtifactSink(),
		Log:       slog.Default(),
	}

	ctx, cancelCause := context.WithCancelCause(context.Background())
	cancelCause(ErrUserCancelled)

	_, _, _, err := step.runRound(ctx, prevPlan, "orig-sid", "", 0, "do the thing", sc)
	if err == nil {
		t.Fatal("expected an error when the outer context is cancelled")
	}
	if !errors.Is(err, ErrUserCancelled) {
		t.Errorf("expected error to wrap the attributed cause ErrUserCancelled, got %v", err)
	}
}
