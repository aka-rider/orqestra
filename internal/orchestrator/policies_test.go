package orchestrator

import (
	"errors"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/harness"
)

// -- loopPolicy ---------------------------------------------------------------

func TestLoopPolicy_NoRepeat(t *testing.T) {
	p := newLoopPolicy(harness.LoopGuardSpec{RepeatThreshold: 3, MaxNudges: 2, CooldownTurns: 1})
	r := p.observe(harness.Event{Kind: harness.EventToolUse, Tool: "Read", Args: `{"path":"/a"}`})
	if r.text != "" || r.stop {
		t.Errorf("distinct tool: expected no action, got %+v", r)
	}
	r = p.observe(harness.Event{Kind: harness.EventToolUse, Tool: "Read", Args: `{"path":"/b"}`})
	if r.text != "" || r.stop {
		t.Errorf("distinct args: expected no action, got %+v", r)
	}
}

func TestLoopPolicy_NudgeAtThreshold(t *testing.T) {
	p := newLoopPolicy(harness.LoopGuardSpec{RepeatThreshold: 3, MaxNudges: 2, CooldownTurns: 1})
	tu := harness.Event{Kind: harness.EventToolUse, Tool: "ExitWorktree", Args: "{}"}
	p.observe(tu)
	p.observe(tu)
	r := p.observe(tu) // 3rd = threshold
	if r.text == "" {
		t.Error("expected nudge at threshold, got none")
	}
	if r.stop {
		t.Error("expected nudge (not stop) at threshold")
	}
}

func TestLoopPolicy_EscalateAfterMaxNudges(t *testing.T) {
	p := newLoopPolicy(harness.LoopGuardSpec{RepeatThreshold: 3, MaxNudges: 1, CooldownTurns: 1})
	tu := harness.Event{Kind: harness.EventToolUse, Tool: "ExitWorktree", Args: "{}"}
	cool := harness.Event{Kind: harness.EventToolUse, Tool: "Glob", Args: "{}"}

	// Trip threshold → nudge 1 (maxNudges reached immediately)
	p.observe(tu)
	p.observe(tu)
	p.observe(tu)
	// Cooldown turn
	p.observe(cool)
	// Second loop → escalate (nudgeCount already == maxNudges)
	p.observe(tu)
	p.observe(tu)
	r := p.observe(tu)
	if !r.stop {
		t.Errorf("expected stop after maxNudges exhausted, got %+v", r)
	}
	if !errors.Is(r.err, ErrLoopEscalated) {
		t.Errorf("expected err to wrap ErrLoopEscalated, got %v", r.err)
	}
}

func TestLoopPolicy_ErrorResultWeight(t *testing.T) {
	p := newLoopPolicy(harness.LoopGuardSpec{RepeatThreshold: 3, MaxNudges: 2, CooldownTurns: 1})
	tu := harness.Event{Kind: harness.EventToolUse, Tool: "ExitWorktree", Args: "{}"}
	tr := harness.Event{Kind: harness.EventToolResult, IsError: true}

	p.observe(tu)      // count=1
	p.observe(tr)      // sets lastError
	r := p.observe(tu) // weight=2 → count=3 = threshold → nudge
	if r.text == "" {
		t.Error("expected nudge with error-weight doubling")
	}
}

// -- silencePolicy ------------------------------------------------------------
//
// silencePolicy only escalates once it has structural evidence of a stall (a
// completed, tool-free assistant turn) — never from elapsed silence alone.
// That's the fix for the mid-generation-ambiguity case: a non-streaming
// provider emits zero events while legitimately still generating.
//
// Productivity is checked LIVE in tick(), not frozen at boundary time —
// internal/harness/stream_event.go's streamEventsFrom emits a message's text
// EventChunk before that same message's EventToolUse, so a combined
// "narrate, then call a tool" turn must not be judged before its own tool
// call has arrived (TestSilencePolicy_CombinedTextAndToolUseSameMessageIsNotEmpty).

func TestSilencePolicy_NoActionWithoutObservedBoundary(t *testing.T) {
	// No observe() call at all — sawBoundary stays false. No matter how long
	// the reported silence is, tick must not nudge or escalate: this is
	// exactly the "still generating, no completed turn yet" ambiguous case.
	p := &silencePolicy{silenceDur: 5 * time.Second, nudgeText: "wake up", maxNudges: 3}
	now := time.Now()
	last := now.Add(-1 * time.Hour)
	r := p.tick(now, last, time.Time{})
	if r.text != "" || r.stop {
		t.Errorf("no observed boundary: expected no action regardless of elapsed silence, got %+v", r)
	}
}

func TestSilencePolicy_ObserveSetsBoundaryOnToolFreeMessage(t *testing.T) {
	p := &silencePolicy{silenceDur: 5 * time.Second, nudgeText: "wake up", maxNudges: 3}
	p.observe(harness.Event{Kind: harness.EventChunk, IsDelta: false}) // whole turn, no tool use
	if !p.sawBoundary || p.toolUseSinceBoundary {
		t.Errorf("expected sawBoundary=true, toolUseSinceBoundary=false after a tool-free boundary, got sawBoundary=%v toolUseSinceBoundary=%v",
			p.sawBoundary, p.toolUseSinceBoundary)
	}
}

func TestSilencePolicy_CombinedTextAndToolUseSameMessageIsNotEmpty(t *testing.T) {
	// Regression: a single assistant message with both narration text and a
	// tool call emits its text EventChunk (the boundary) before its own
	// EventToolUse. Deciding "empty" at boundary time would misclassify this
	// extremely common pattern as a stall. tick() must stay dormant once the
	// trailing tool call has been observed, no matter how much silence follows.
	p := &silencePolicy{silenceDur: 5 * time.Second, nudgeText: "wake up", maxNudges: 3}
	p.observe(harness.Event{Kind: harness.EventChunk, IsDelta: false, Text: "Let me check this."})
	p.observe(harness.Event{Kind: harness.EventToolUse, Tool: "Read"}) // same message, arrives right after

	now := time.Now()
	last := now.Add(-1 * time.Hour)
	r := p.tick(now, last, time.Time{})
	if r.text != "" || r.stop {
		t.Errorf("combined text+tool_use message misclassified as empty, got %+v", r)
	}
}

func TestSilencePolicy_NudgesAfterObservedEmptyTurn(t *testing.T) {
	p := &silencePolicy{silenceDur: 5 * time.Second, nudgeText: "wake up", maxNudges: 3}
	p.observe(harness.Event{Kind: harness.EventChunk, IsDelta: false})
	now := time.Now()
	last := now.Add(-6 * time.Second) // 6s ≥ 5s
	r := p.tick(now, last, time.Time{})
	if r.text != "wake up" {
		t.Errorf("after empty turn, at threshold: expected nudge, got %+v", r)
	}
}

func TestSilencePolicy_DoesNotReNudgeWithinCooldown(t *testing.T) {
	p := &silencePolicy{silenceDur: 5 * time.Second, nudgeText: "wake up", maxNudges: 3}
	p.observe(harness.Event{Kind: harness.EventChunk, IsDelta: false})
	now := time.Now()
	last := now.Add(-6 * time.Second)
	p.tick(now, last, time.Time{})      // first nudge — arms lastNudge
	r := p.tick(now, last, time.Time{}) // same instant — still within cooldown
	if r.text != "" {
		t.Errorf("within cooldown: expected no nudge, got %q", r.text)
	}
}

func TestSilencePolicy_ReNudgesAfterAnotherSilenceDur(t *testing.T) {
	p := &silencePolicy{silenceDur: 5 * time.Second, nudgeText: "wake up", maxNudges: 3}
	p.observe(harness.Event{Kind: harness.EventChunk, IsDelta: false})
	now := time.Now()
	last := now.Add(-6 * time.Second)
	p.tick(now, last, time.Time{}) // first nudge
	later := now.Add(5 * time.Second)
	r := p.tick(later, last, time.Time{}) // 5s after first nudge = next window
	if r.text != "wake up" {
		t.Errorf("after cooldown: expected nudge, got %q", r.text)
	}
}

func TestSilencePolicy_EscalatesAfterMaxNudges(t *testing.T) {
	p := &silencePolicy{silenceDur: 5 * time.Second, nudgeText: "wake up", maxNudges: 2}
	p.observe(harness.Event{Kind: harness.EventChunk, IsDelta: false})
	now := time.Now()
	last := now.Add(-6 * time.Second)
	if r := p.tick(now, last, time.Time{}); r.stop {
		t.Fatalf("expected nudge (1st), not escalate: %+v", r)
	}
	if r := p.tick(now.Add(5*time.Second), last, time.Time{}); r.stop {
		t.Fatalf("expected nudge (2nd), not escalate: %+v", r)
	}
	r := p.tick(now.Add(10*time.Second), last, time.Time{})
	if !r.stop {
		t.Errorf("expected escalate after maxNudges exhausted, got %+v", r)
	}
	if !errors.Is(r.err, ErrSilenceEscalated) {
		t.Errorf("expected err to wrap ErrSilenceEscalated, got %v", r.err)
	}
}

func TestSilencePolicy_RecoveryResetsAfterProductiveTurn(t *testing.T) {
	p := &silencePolicy{silenceDur: 5 * time.Second, nudgeText: "wake up", maxNudges: 3}
	p.observe(harness.Event{Kind: harness.EventChunk, IsDelta: false}) // empty turn
	now := time.Now()
	last := now.Add(-6 * time.Second)
	p.tick(now, last, time.Time{}) // nudge 1

	// Model recovers: calls a tool (no further boundary needed to credit it).
	p.observe(harness.Event{Kind: harness.EventToolUse, Tool: "Read"})
	if !p.toolUseSinceBoundary || p.nudgeCount != 0 {
		t.Errorf("expected reset after recovery tool call, got toolUseSinceBoundary=%v nudgeCount=%d",
			p.toolUseSinceBoundary, p.nudgeCount)
	}

	// Further silence must not nudge/escalate while a tool call is active
	// since the last boundary.
	r := p.tick(now.Add(20*time.Second), last, time.Time{})
	if r.text != "" || r.stop {
		t.Errorf("expected no action while toolUseSinceBoundary is true, got %+v", r)
	}
}

func TestSilencePolicy_RepeatedEmptyTurnsStillEscalate(t *testing.T) {
	// A model that keeps giving empty (non-tool) responses even after being
	// nudged must still exhaust its budget and escalate — a fresh tool-free
	// boundary must NOT reset nudgeCount, or this regresses into the original
	// infinite-nudge-loop bug the bound was added to fix.
	p := &silencePolicy{silenceDur: 5 * time.Second, nudgeText: "wake up", maxNudges: 2}
	p.observe(harness.Event{Kind: harness.EventChunk, IsDelta: false}) // empty turn 1
	now := time.Now()
	last := now.Add(-6 * time.Second)
	if r := p.tick(now, last, time.Time{}); r.stop || r.text == "" {
		t.Fatalf("expected nudge (1st), got %+v", r)
	}

	// Model responds again — still no tool call. A new boundary, still empty.
	p.observe(harness.Event{Kind: harness.EventChunk, IsDelta: false})
	if r := p.tick(now.Add(5*time.Second), last, time.Time{}); r.stop || r.text == "" {
		t.Fatalf("expected nudge (2nd), got %+v", r)
	}

	p.observe(harness.Event{Kind: harness.EventChunk, IsDelta: false})
	r := p.tick(now.Add(10*time.Second), last, time.Time{})
	if !r.stop {
		t.Errorf("expected escalate after repeated empty turns exhausted the budget, got %+v", r)
	}
}

// -- preTimeoutPolicy ---------------------------------------------------------

func TestPreTimeoutPolicy_NoDeadline(t *testing.T) {
	p := &preTimeoutPolicy{nudgeText: "hurry"}
	r := p.tick(time.Now(), time.Now(), time.Time{}) // zero deadline
	if r.text != "" {
		t.Errorf("no deadline: expected no nudge, got %q", r.text)
	}
}

func TestPreTimeoutPolicy_FiresWithinWarning(t *testing.T) {
	p := &preTimeoutPolicy{nudgeText: "hurry"}
	now := time.Now()
	deadline := now.Add(30 * time.Second) // 30s < preTimeoutWarning (60s)
	r := p.tick(now, now, deadline)
	if r.text != "hurry" {
		t.Errorf("within warning: expected nudge, got %q", r.text)
	}
}

func TestPreTimeoutPolicy_DoesNotFireBeforeWarning(t *testing.T) {
	p := &preTimeoutPolicy{nudgeText: "hurry"}
	now := time.Now()
	deadline := now.Add(90 * time.Second) // 90s > preTimeoutWarning (60s)
	r := p.tick(now, now, deadline)
	if r.text != "" {
		t.Errorf("before warning: expected no nudge, got %q", r.text)
	}
}

func TestPreTimeoutPolicy_FiresOnce(t *testing.T) {
	p := &preTimeoutPolicy{nudgeText: "hurry"}
	now := time.Now()
	deadline := now.Add(10 * time.Second)
	p.tick(now, now, deadline) // fires
	r := p.tick(now, now, deadline)
	if r.text != "" {
		t.Errorf("second tick: expected no nudge (already fired), got %q", r.text)
	}
}

// -- driftPolicy --------------------------------------------------------------

func TestDriftPolicy_NudgesOnImplementationIntent(t *testing.T) {
	p := &driftPolicy{nudgeText: "submit now"}
	ev := harness.Event{Kind: harness.EventChunk, Text: "I'll implement this next", IsDelta: false}
	r := p.observe(ev)
	if r.text != "submit now" {
		t.Errorf("implementation intent: expected nudge, got %q", r.text)
	}
}

func TestDriftPolicy_IgnoresDelta(t *testing.T) {
	p := &driftPolicy{nudgeText: "submit now"}
	ev := harness.Event{Kind: harness.EventChunk, Text: "I'll implement this next", IsDelta: true}
	r := p.observe(ev)
	if r.text != "" {
		t.Errorf("delta chunk: expected no nudge, got %q", r.text)
	}
}

func TestDriftPolicy_NudgesOnce(t *testing.T) {
	p := &driftPolicy{nudgeText: "submit now"}
	ev := harness.Event{Kind: harness.EventChunk, Text: "Let me implement this", IsDelta: false}
	p.observe(ev)
	r := p.observe(ev) // second occurrence
	if r.text != "" {
		t.Errorf("second intent: expected no nudge (already nudged), got %q", r.text)
	}
}

func TestDriftPolicy_StandDownOnSubmitReport(t *testing.T) {
	p := &driftPolicy{nudgeText: "submit now"}
	// Stand down via SubmitReport tool-use
	p.observe(harness.Event{Kind: harness.EventToolUse, Tool: "mcp__orqestra__SubmitReport"})
	// Now drift intent is observed but policy is stood down
	r := p.observe(harness.Event{Kind: harness.EventChunk, Text: "I'll implement this", IsDelta: false})
	if r.text != "" {
		t.Errorf("after stand-down: expected no nudge, got %q", r.text)
	}
}

func TestDriftPolicy_NoNudgeWithoutIntent(t *testing.T) {
	p := &driftPolicy{nudgeText: "submit now"}
	r := p.observe(harness.Event{Kind: harness.EventChunk, Text: "The codebase has three packages.", IsDelta: false})
	if r.text != "" {
		t.Errorf("no intent: expected no nudge, got %q", r.text)
	}
}
