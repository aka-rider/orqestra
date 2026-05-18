package orchestrator

import (
	"sync"
	"testing"
)

func TestStreamRing_AppendAndSnapshot(t *testing.T) {
	r := NewStreamRing(100)
	r.SetAgent("researcher")

	r.Append(StreamEntry{Kind: EntryText, Text: "hello"})
	r.AppendActivity("Read", "go.mod")
	r.AppendStats(100, 50)

	agentID, entries := r.Snapshot()
	if agentID != "researcher" {
		t.Errorf("agentID = %q, want researcher", agentID)
	}
	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3", len(entries))
	}
	if entries[0].Kind != EntryText || entries[0].Text != "hello" {
		t.Errorf("entries[0] = %+v, want EntryText hello", entries[0])
	}
	if entries[1].Kind != EntryToolUse || entries[1].Tool != "Read" {
		t.Errorf("entries[1] = %+v, want EntryToolUse Read", entries[1])
	}
	if entries[2].Kind != EntryStats || !entries[2].Stats.Valid || entries[2].Stats.Input != 100 {
		t.Errorf("entries[2] = %+v, want EntryStats{100,50,true}", entries[2])
	}
}

func TestStreamRing_SetAgentSnapshots(t *testing.T) {
	r := NewStreamRing(100)
	r.SetAgent("researcher")
	r.AppendActivity("Read", "file1.txt")
	r.AppendActivity("Read", "file2.txt")

	r.SetAgent("architect")
	r.AppendActivity("Write", "plan.md")

	// Researcher snapshot preserved
	acts := r.AgentActivities("researcher")
	if len(acts) != 2 {
		t.Errorf("researcher activities = %d, want 2", len(acts))
	}

	// Current agent
	acts = r.AgentActivities("architect")
	if len(acts) != 1 {
		t.Errorf("architect activities = %d, want 1", len(acts))
	}

	// Nonexistent
	acts = r.AgentActivities("unknown")
	if acts != nil {
		t.Errorf("unknown activities = %v, want nil", acts)
	}
}

func TestStreamRing_Overflow(t *testing.T) {
	r := NewStreamRing(5)
	r.SetAgent("test")
	for i := 0; i < 10; i++ {
		r.AppendActivity("Read", "file")
	}

	_, entries := r.Snapshot()
	if len(entries) != 5 {
		t.Errorf("len(entries) = %d, want 5 (capacity)", len(entries))
	}
}

func TestStreamRing_AppendText_LineAccumulation(t *testing.T) {
	r := NewStreamRing(100)
	r.SetAgent("test")

	r.AppendText("hel")
	r.AppendText("lo\nworld\n")

	_, entries := r.Snapshot()
	var lines []string
	for _, e := range entries {
		if e.Kind == EntryText {
			lines = append(lines, e.Text)
		}
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %v, want [hello world]", lines)
	}
	if lines[0] != "hello" {
		t.Errorf("lines[0] = %q, want hello", lines[0])
	}
	if lines[1] != "world" {
		t.Errorf("lines[1] = %q, want world", lines[1])
	}
}

func TestStreamRing_AppendText_DropsRawFrames(t *testing.T) {
	r := NewStreamRing(100)
	r.SetAgent("test")

	r.AppendText(`{"type":"assistant","message":{"content":[{"type":"text","text":"ignored"}]}}` + "\n")
	r.AppendText("hello\n")
	r.AppendText(`{"type":"result","subtype":"success","result":"ignored"}` + "\n")

	_, entries := r.Snapshot()
	var lines []string
	for _, e := range entries {
		if e.Kind == EntryText {
			lines = append(lines, e.Text)
		}
	}
	if len(lines) != 1 || lines[0] != "hello" {
		t.Errorf("lines = %v, want [hello]", lines)
	}
}

func TestStreamRing_AppendText_PassesLooseBraces(t *testing.T) {
	r := NewStreamRing(100)
	r.SetAgent("test")
	r.AppendText("{not json\n")

	_, entries := r.Snapshot()
	var found bool
	for _, e := range entries {
		if e.Kind == EntryText && e.Text == "{not json" {
			found = true
		}
	}
	if !found {
		t.Error("expected loose-brace line to pass through")
	}
}

func TestStreamRing_SnapshotCompat(t *testing.T) {
	r := NewStreamRing(100)
	r.SetAgent("worker")
	r.AppendText("line1\nline2\n")
	r.AppendActivity("Bash", "ls")
	r.AppendStats(200, 100)

	agentID, lines, activities := r.SnapshotCompat()
	if agentID != "worker" {
		t.Errorf("agentID = %q, want worker", agentID)
	}
	if len(lines) != 2 {
		t.Errorf("lines = %v, want 2 lines", lines)
	}
	if len(activities) != 1 || activities[0].Tool != "Bash" {
		t.Errorf("activities = %v, want [{Bash ls}]", activities)
	}
}

func TestStreamRing_ConcurrentAccess(t *testing.T) {
	r := NewStreamRing(100)
	r.SetAgent("test")

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			r.AppendText("line\n")
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			r.AppendActivity("Read", "file")
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			r.Snapshot()
		}
	}()

	wg.Wait()
}
