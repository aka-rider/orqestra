package orchestrator

import "testing"

func TestStreamBuffer_AppendActivity(t *testing.T) {
	sb := NewStreamBuffer(200)
	sb.SetAgent("worker")

	sb.AppendActivity("Read", "go.mod")
	sb.AppendActivity("Bash", "ls -la")

	agentID, _, acts := sb.Snapshot()
	if agentID != "worker" {
		t.Errorf("agentID = %q, want worker", agentID)
	}
	if len(acts) != 2 {
		t.Fatalf("len(activities) = %d, want 2", len(acts))
	}
	if acts[0].Tool != "Read" || acts[0].Detail != "go.mod" {
		t.Errorf("activity[0] = %+v, want {Read go.mod}", acts[0])
	}
	if acts[1].Tool != "Bash" || acts[1].Detail != "ls -la" {
		t.Errorf("activity[1] = %+v, want {Bash ls -la}", acts[1])
	}
}

func TestStreamBuffer_SetAgentClearsActivities(t *testing.T) {
	sb := NewStreamBuffer(200)
	sb.SetAgent("worker")
	sb.AppendActivity("Read", "go.mod")
	sb.SetAgent("architect")

	_, _, acts := sb.Snapshot()
	if len(acts) != 0 {
		t.Errorf("activities not cleared on SetAgent, got %d", len(acts))
	}
}

func TestStreamBuffer_ActivityRingOverflow(t *testing.T) {
	sb := NewStreamBuffer(200)
	sb.SetAgent("worker")
	for i := 0; i < 30; i++ {
		sb.AppendActivity("Read", "file")
	}

	_, _, acts := sb.Snapshot()
	if len(acts) != maxActivities {
		t.Errorf("len(activities) = %d, want %d (maxActivities)", len(acts), maxActivities)
	}
}

func TestStreamWriter_ImplementsActivitySink(t *testing.T) {
	sb := NewStreamBuffer(200)
	sb.SetAgent("test")
	w := &streamWriter{buf: sb}

	// Verify it satisfies the interface by calling OnToolUse
	w.OnToolUse("Read", "go.mod")

	_, _, acts := sb.Snapshot()
	if len(acts) != 1 {
		t.Fatalf("expected 1 activity, got %d", len(acts))
	}
	if acts[0].Tool != "Read" {
		t.Errorf("tool = %q, want Read", acts[0].Tool)
	}
}
func TestStreamBuffer_AgentSnapshots(t *testing.T) {
	sb := NewStreamBuffer(10)

	// Test nonexistent
	if acts := sb.AgentActivities("nonexistent"); acts != nil {
		t.Errorf("expected nil for nonexistent agent, got %v", acts)
	}

	sb.SetAgent("researcher")
	sb.AppendActivity("Read", "file1.txt")
	sb.AppendActivity("Read", "file2.txt")

	// Test current agent activities
	if acts := sb.AgentActivities("researcher"); len(acts) != 2 {
		t.Errorf("expected 2 activities for current agent, got %d", len(acts))
	}

	sb.SetAgent("architect")
	if acts := sb.AgentActivities("researcher"); len(acts) != 2 {
		t.Errorf("expected 2 activities for saved agent snapshot, got %d", len(acts))
	}

	if acts := sb.AgentActivities("architect"); len(acts) != 0 {
		t.Errorf("expected 0 activities for new agent, got %d", len(acts))
	}
}

func TestStreamBuffer_DropsRawStreamFrames(t *testing.T) {
	sb := NewStreamBuffer(50)
	input := `{"type":"assistant","message":{"content":[{"type":"text","text":"ignored"}]}}` + "\n" +
		"hello" + "\n" +
		`{"type":"result","subtype":"success","result":"ignored"}` + "\n"
	sb.Append(input)

	_, lines, _ := sb.Snapshot()
	for _, line := range lines {
		if t2, ok := looksLikeStreamEventFrame(line); ok {
			t.Errorf("snapshot retained raw stream-event frame (type=%q): %q", t2, line)
		}
	}
	var found bool
	for _, line := range lines {
		if line == "hello" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected plain text %q in snapshot, got %v", "hello", lines)
	}
}

func TestStreamBuffer_PassesThroughLooseBraces(t *testing.T) {
	sb := NewStreamBuffer(50)
	sb.Append("{not json\n")

	_, lines, _ := sb.Snapshot()
	var found bool
	for _, line := range lines {
		if line == "{not json" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected loose-brace line %q to pass through, got %v", "{not json", lines)
	}
}
