package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/orchestrator"
)

func TestFileHyperlink_AbsolutePath(t *testing.T) {
	path := "/Users/dev/internal/model.go"
	out := fileHyperlink(path, "/Users/default")

	if !strings.Contains(out, path) {
		t.Errorf("expected output to contain '%s' as visible text, got %q", path, out)
	}
	if !strings.Contains(out, "file:///Users/dev/internal/model.go") {
		t.Errorf("expected URI to be absolute, got %q", out)
	}
}

func TestFileHyperlink_RelativePath(t *testing.T) {
	path := "internal/model.go"
	cwd := "/Users/dev"
	out := fileHyperlink(path, cwd)

	if !strings.HasSuffix(out, "\033\\internal/model.go\033]8;;\033\\") {
		t.Errorf("expected output to end with visible text '%s', got %q", path, out)
	}
	if !strings.Contains(out, "file:///Users/dev/internal/model.go") {
		t.Errorf("expected URI to be absolute based on cwd, got %q", out)
	}
}

func setupTestPipelineScreen() PipelineScreen {
	s := NewPipelineScreen("test")
	s.cwd = "/test/dir"
	sb := orchestrator.NewStreamBuffer(50)
	sb.SetAgent("researcher")
	sb.Append("stream line 1\n")
	sb.Append("stream line 2\n")
	sb.Append("stream line 3\n")
	sb.Append("stream line 4\n")
	sb.Append("stream line 5\n")
	sb.Append("stream line 6\n") // > 5 lines to test preview

	sb.AppendActivity("Read", "file1.txt")
	sb.AppendActivity("Bash", "ls -l")

	s.SetStreamBuf(sb)
	s.agents = []AgentRow{
		{ID: "researcher", State: "done", Elapsed: time.Second, InputTokens: 100, OutputTokens: 50},
		{ID: "architect", State: "running", StartedAt: time.Now()},
	}
	s.focusedAgent = 1
	return s
}

func TestViewStreaming_NoRawDump(t *testing.T) {
	s := setupTestPipelineScreen()

	out := s.viewStreaming(120)

	if !strings.Contains(out, "Read") || !strings.Contains(out, "Bash") {
		t.Errorf("expected activity names, got %s", out)
	}

	if strings.Contains(out, "stream line 1") {
		t.Errorf("expected oldest stream lines to be truncated")
	}
	if !strings.Contains(out, "stream line 6") {
		t.Errorf("expected newest stream lines to be visible")
	}
}

func TestViewStreaming_FilePathsAreFullPaths(t *testing.T) {
	s := setupTestPipelineScreen()

	out := s.viewStreaming(120)

	if !strings.Contains(out, "file1.txt") {
		t.Errorf("expected relative path to remain visible, got %s", out)
	}
	if !strings.Contains(out, "file:///test/dir/file1.txt") {
		t.Errorf("expected absolute OSC 8 URI, got %s", out)
	}
}

func TestViewAgentHistory_ShowsActivities(t *testing.T) {
	s := setupTestPipelineScreen()
	// trigger snapshot of researcher
	s.streamBuf.SetAgent("architect")

	out := s.viewAgentHistory(120)

	if !strings.Contains(out, "Read") || !strings.Contains(out, "file1.txt") {
		t.Errorf("expected activity in agent history, got %s", out)
	}
}

func TestViewCompletion_ShowsAgentSummary(t *testing.T) {
	s := setupTestPipelineScreen()
	s.streamBuf.SetAgent("architect") // Snapshot researcher

	out := s.viewCompletion(120)

	if !strings.Contains(out, "researcher") || !strings.Contains(out, "architect") {
		t.Errorf("expected agents in completion summary, got %s", out)
	}

	if !strings.Contains(out, "file1.txt") {
		t.Errorf("expected file activity in completion summary, got %s", out)
	}
}
