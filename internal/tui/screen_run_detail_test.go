package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/orchestrator"
	"github.com/xiii/orqestra/internal/tui/keymap"
)

func TestRunDetailScrollFollow(t *testing.T) {
	s := NewRunDetailScreen(keymap.Default())
	steps := make([]orchestrator.StepMeta, 10)
	for i := range steps {
		steps[i] = orchestrator.StepMeta{
			AgentID:      fmt.Sprintf("agent-%d", i),
			ModelDisplay: "test-model",
			InputTokens:  100,
			OutputTokens: 200,
			StartTime:    time.Now(),
			EndTime:      time.Now().Add(time.Minute),
			Status:       "done",
		}
	}
	s.SetDetail(orchestrator.RunDetail{Steps: steps})
	s.stepsVP.SetWidth(40)
	s.stepsVP.SetHeight(8) // small: fits ~1 card

	for i := 0; i < 5; i++ {
		s.stepCursor++
		s.SyncViewports()
	}
	if s.stepsVP.YOffset() == 0 {
		t.Error("expected stepsVP to scroll down when cursor moves past viewport")
	}

	for i := 0; i < 5; i++ {
		s.stepCursor--
		s.SyncViewports()
	}
	if s.stepsVP.YOffset() != 0 {
		t.Errorf("expected stepsVP YOffset=0 at step 0, got %d", s.stepsVP.YOffset())
	}
}

func TestRunDetailViewPurity(t *testing.T) {
	s := NewRunDetailScreen(keymap.Default())
	s.stepsVP.SetWidth(40)
	s.stepsVP.SetHeight(20)
	s.detailVP.SetWidth(80)
	s.detailVP.SetHeight(20)
	s.logVP.SetWidth(80)
	s.logVP.SetHeight(8)
	// Provide enough content lines so YOffset=3 is not clamped to 0.
	s.stepsVP.SetContent(strings.Repeat("line\n", 50))
	s.stepsVP.SetYOffset(3)
	before := s.stepsVP.YOffset()
	if before == 0 {
		t.Fatal("precondition: expected YOffset > 0 after setting content and offset")
	}
	_ = s.View(120, 34)
	_ = s.View(120, 34)
	if s.stepsVP.YOffset() != before {
		t.Errorf("View() mutated stepsVP.YOffset: %d → %d", before, s.stepsVP.YOffset())
	}
}

func TestRunDetailPerStepContent(t *testing.T) {
	dir := t.TempDir()
	p0 := filepath.Join(dir, "plan0.md")
	p1 := filepath.Join(dir, "plan1.md")
	if err := os.WriteFile(p0, []byte("# Plan Zero"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p1, []byte("# Plan One"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := NewRunDetailScreen(keymap.Default())
	s.detailVP.SetWidth(80)
	s.detailVP.SetHeight(20)
	s.stepsVP.SetWidth(40)
	s.stepsVP.SetHeight(20)
	s.logVP.SetWidth(80)
	s.logVP.SetHeight(8)
	s.SetDetail(orchestrator.RunDetail{
		Steps: []orchestrator.StepMeta{
			{AgentID: "a0", ClaudePlanFilePath: p0, Status: "done"},
			{AgentID: "a1", ClaudePlanFilePath: p1, Status: "done"},
		},
	})

	s.SyncViewports()
	content0 := s.detailVP.View()

	s.stepCursor = 1
	s.SyncViewports()
	content1 := s.detailVP.View()

	if content0 == content1 {
		t.Error("detailVP content did not change when switching steps")
	}
}
