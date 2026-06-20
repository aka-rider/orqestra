package orchestrator

import (
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
	p.observe(tu); p.observe(tu); p.observe(tu)
	// Cooldown turn
	p.observe(cool)
	// Second loop → escalate (nudgeCount already == maxNudges)
	p.observe(tu); p.observe(tu)
	r := p.observe(tu)
	if !r.stop {
		t.Errorf("expected stop after maxNudges exhausted, got %+v", r)
	}
}

func TestLoopPolicy_ErrorResultWeight(t *testing.T) {
	p := newLoopPolicy(harness.LoopGuardSpec{RepeatThreshold: 3, MaxNudges: 2, CooldownTurns: 1})
	tu := harness.Event{Kind: harness.EventToolUse, Tool: "ExitWorktree", Args: "{}"}
	tr := harness.Event{Kind: harness.EventToolResult, IsError: true}

	p.observe(tu)         // count=1
	p.observe(tr)         // sets lastError
	r := p.observe(tu)    // weight=2 → count=3 = threshold → nudge
	if r.text == "" {
		t.Error("expected nudge with error-weight doubling")
	}
}

// -- silencePolicy ------------------------------------------------------------

func TestSilencePolicy_BelowThreshold(t *testing.T) {
	p := &silencePolicy{silenceDur: 5 * time.Second, nudgeText: "wake up"}
	now := time.Now()
	last := now.Add(-3 * time.Second) // 3s < 5s
	r := p.tick(now, last, time.Time{})
	if r.text != "" {
		t.Errorf("below threshold: expected no nudge, got %q", r.text)
	}
}

func TestSilencePolicy_AtOrAboveThreshold(t *testing.T) {
	p := &silencePolicy{silenceDur: 5 * time.Second, nudgeText: "wake up"}
	now := time.Now()
	last := now.Add(-6 * time.Second) // 6s ≥ 5s
	r := p.tick(now, last, time.Time{})
	if r.text != "wake up" {
		t.Errorf("at threshold: expected nudge, got %q", r.text)
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
