package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/plan"
)

func buildTestPlanHistoryRepo(t *testing.T, commits []string) (string, string) {
	t.Helper()
	repo, err := plan.NewGitRepo(t.TempDir())
	if err != nil {
		t.Fatalf("NewGitRepo: %v", err)
	}
	for _, c := range commits {
		if err := repo.CommitPlan(c, "user: commit "+c); err != nil {
			t.Fatalf("CommitPlan: %v", err)
		}
	}
	head, err := repo.HeadCommitHash()
	if err != nil {
		t.Fatalf("HeadCommitHash: %v", err)
	}
	return repo.Dir(), head
}

func dispatch(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	return cmd()
}

func keyMsg(s string) tea.KeyPressMsg {
	switch s {
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "pgdn":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	}
	if len(s) == 1 {
		return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
	}
	// Fall-through (e.g. "ctrl+y") — caller must set modifiers explicitly.
	return tea.KeyPressMsg{Text: s}
}

func TestPlanHistoryView_RenderPurity(t *testing.T) {
	dir, head := buildTestPlanHistoryRepo(t, []string{"a", "b", "c"})
	s := NewPlanHistoryScreen()
	cmd := s.Open(dir, false, head)
	s.RecalculateLayout(80, 24)
	msg := dispatch(t, cmd)
	s, _ = s.Update(msg)
	out1 := s.View(80, 24)
	prevOffset := s.rightVP.YOffset()
	out2 := s.View(80, 24)
	if out1 != out2 {
		t.Errorf("View output not stable across calls")
	}
	if s.rightVP.YOffset() != prevOffset {
		t.Errorf("View mutated rightVP YOffset: %d → %d", prevOffset, s.rightVP.YOffset())
	}
}

func TestPlanHistoryUpdate_CursorMoveFiresDetailLoad(t *testing.T) {
	dir, head := buildTestPlanHistoryRepo(t, []string{"a", "b", "c"})
	s := NewPlanHistoryScreen()
	cmd := s.Open(dir, false, head)
	s.RecalculateLayout(80, 24)
	s, _ = s.Update(dispatch(t, cmd))
	s, detailCmd := s.Update(keyMsg("down"))
	if detailCmd == nil {
		t.Fatal("expected detail load command after cursor move")
	}
	out := detailCmd()
	if _, ok := out.(planRevisionDetailLoadedMsg); !ok {
		t.Fatalf("expected planRevisionDetailLoadedMsg, got %T", out)
	}
}

func TestPlanHistoryUpdate_RevertEmitsIntent(t *testing.T) {
	dir, head := buildTestPlanHistoryRepo(t, []string{"a", "b", "c"})
	s := NewPlanHistoryScreen()
	cmd := s.Open(dir, false, head)
	s.RecalculateLayout(80, 24)
	s, _ = s.Update(dispatch(t, cmd))
	// HEAD is newest; move cursor down to an older revision.
	s, dc := s.Update(keyMsg("down"))
	if dc != nil {
		s, _ = s.Update(dispatch(t, dc))
	}
	// Press r → revert prompt opens (detail is loaded)
	s, _ = s.Update(keyMsg("r"))
	if !s.revertPrompt {
		t.Fatal("expected revertPrompt to be open")
	}
	// Press enter → emit RevertPlanIntent
	s, _ = s.Update(keyMsg("enter"))
	if s.PendingIntent == nil {
		t.Fatal("expected PendingIntent after enter")
	}
	intent, ok := s.PendingIntent.(RevertPlanIntent)
	if !ok {
		t.Fatalf("expected RevertPlanIntent, got %T", s.PendingIntent)
	}
	if intent.Content == "" {
		t.Error("RevertPlanIntent.Content should not be empty")
	}
	if intent.ShortSHA == "" {
		t.Error("RevertPlanIntent.ShortSHA should not be empty")
	}
}

func TestPlanHistoryUpdate_RevertPendingBeforeDetail(t *testing.T) {
	dir, head := buildTestPlanHistoryRepo(t, []string{"a", "b"})
	s := NewPlanHistoryScreen()
	cmd := s.Open(dir, false, head)
	s.RecalculateLayout(80, 24)
	s, _ = s.Update(dispatch(t, cmd))
	// Move cursor without firing the detail load yet → pretend detail is loading.
	s.cursor = 1
	s.detailLoad = true
	s.detailSHA = s.revisions[1].SHA
	// Press r before detail arrives: pendingRevertSHA gets set, no prompt yet.
	s, _ = s.Update(keyMsg("r"))
	if s.revertPrompt {
		t.Fatal("revertPrompt should not be open before detail arrives")
	}
	if s.pendingRevertSHA != s.revisions[1].SHA {
		t.Fatal("pendingRevertSHA should have been stashed")
	}
	// Deliver detail
	s, _ = s.Update(planRevisionDetailLoadedMsg{SHA: s.revisions[1].SHA, Content: "x"})
	if !s.revertPrompt {
		t.Fatal("revertPrompt should open after detail arrives")
	}
}

func TestPlanHistoryUpdate_ReadOnlyNoRevert(t *testing.T) {
	dir, head := buildTestPlanHistoryRepo(t, []string{"a", "b"})
	s := NewPlanHistoryScreen()
	cmd := s.Open(dir, true, head) // read-only
	s.RecalculateLayout(80, 24)
	s, _ = s.Update(dispatch(t, cmd))
	s, dc := s.Update(keyMsg("down"))
	if dc != nil {
		s, _ = s.Update(dispatch(t, dc))
	}
	s, _ = s.Update(keyMsg("r"))
	if s.revertPrompt {
		t.Error("revertPrompt must remain closed in read-only mode")
	}
	if s.PendingIntent != nil {
		t.Error("no intent expected in read-only mode")
	}
}

func TestPlanHistoryUpdate_EscClosesViewer(t *testing.T) {
	dir, head := buildTestPlanHistoryRepo(t, []string{"a"})
	s := NewPlanHistoryScreen()
	cmd := s.Open(dir, false, head)
	s.RecalculateLayout(80, 24)
	s, _ = s.Update(dispatch(t, cmd))
	s, _ = s.Update(keyMsg("esc"))
	if _, ok := s.PendingIntent.(ClosePlanHistoryIntent); !ok {
		t.Fatalf("expected ClosePlanHistoryIntent, got %T", s.PendingIntent)
	}
}

func TestPlanHistoryLayout_SmallTerminal(t *testing.T) {
	s := NewPlanHistoryScreen()
	s.RecalculateLayout(20, 5)
	out := s.View(20, 5)
	if !strings.Contains(out, "Terminal too small") {
		t.Errorf("expected small-terminal message, got %q", out)
	}
}

func TestPlanHistoryUpdate_RevertOnHEADNoOp(t *testing.T) {
	dir, head := buildTestPlanHistoryRepo(t, []string{"a", "b", "c"})
	s := NewPlanHistoryScreen()
	cmd := s.Open(dir, false, head)
	s.RecalculateLayout(80, 24)
	s, _ = s.Update(dispatch(t, cmd))
	// Cursor stays at 0 (HEAD). Press r.
	s, _ = s.Update(keyMsg("r"))
	if s.revertPrompt {
		t.Error("revertPrompt must remain closed on HEAD")
	}
}

func TestPlanHistoryScroll_PaneSwitchPreservesOffset(t *testing.T) {
	dir, head := buildTestPlanHistoryRepo(t, []string{strings.Repeat("a\n", 50), strings.Repeat("b\n", 50)})
	s := NewPlanHistoryScreen()
	cmd := s.Open(dir, false, head)
	s.RecalculateLayout(80, 24)
	s, _ = s.Update(dispatch(t, cmd))
	// Move cursor to non-HEAD so there's a diff
	s, dc := s.Update(keyMsg("down"))
	if dc != nil {
		s, _ = s.Update(dispatch(t, dc))
	}
	// Manually scroll right viewport
	s.rightVP.SetYOffset(3)
	desired := s.rightVP.YOffset()
	if desired == 0 {
		t.Skip("viewport refused to scroll (content too short for offset to stick)")
	}
	// Switch pane d → f → d, offset must survive each pane switch on same revision
	s, _ = s.Update(keyMsg("f"))
	if s.rightVP.YOffset() != desired {
		t.Errorf("YOffset reset on pane switch d→f: want %d got %d", desired, s.rightVP.YOffset())
	}
	s, _ = s.Update(keyMsg("d"))
	if s.rightVP.YOffset() != desired {
		t.Errorf("YOffset reset on pane switch f→d: want %d got %d", desired, s.rightVP.YOffset())
	}
}

func TestPlanHistoryScroll_RevisionChangeResetsOffset(t *testing.T) {
	dir, head := buildTestPlanHistoryRepo(t, []string{"a\n", "b\n", "c\n"})
	s := NewPlanHistoryScreen()
	cmd := s.Open(dir, false, head)
	s.RecalculateLayout(80, 24)
	s, _ = s.Update(dispatch(t, cmd))
	s.rightVP.SetYOffset(2)
	s, dc := s.Update(keyMsg("down"))
	if dc == nil {
		t.Fatal("expected detail load on cursor move")
	}
	// Deliver the detail message - SyncViewports(true) should reset offset
	s, _ = s.Update(dispatch(t, dc))
	if s.rightVP.YOffset() != 0 {
		t.Errorf("YOffset should reset on revision change: got %d", s.rightVP.YOffset())
	}
}

func TestPlanHistoryUpdate_LoadErrorRendersBanner(t *testing.T) {
	s := NewPlanHistoryScreen()
	s.RecalculateLayout(80, 24)
	s.loading = true
	s, _ = s.Update(planRevisionsLoadedMsg{Err: errors.New("plan-history missing")})
	out := s.View(80, 24)
	if !strings.Contains(out, "plan-history missing") {
		t.Errorf("expected error banner; got %q", out)
	}
}

func TestPlanHistoryUpdate_DetailErrorRendersInRightPane(t *testing.T) {
	dir, head := buildTestPlanHistoryRepo(t, []string{"a", "b"})
	s := NewPlanHistoryScreen()
	cmd := s.Open(dir, false, head)
	s.RecalculateLayout(80, 24)
	s, _ = s.Update(dispatch(t, cmd))
	// Force a detail-error message that matches the currently selected sha
	s, _ = s.Update(planRevisionDetailLoadedMsg{SHA: s.detailSHA, Err: errors.New("rev not found")})
	if s.loadErr != nil {
		t.Errorf("loadErr should remain nil; got %v", s.loadErr)
	}
	out := s.View(80, 24)
	if !strings.Contains(out, "rev not found") {
		t.Errorf("expected detail error in output; got %q", out)
	}
}

func TestPlanHistoryHandleMouse_RoutesByBounds(t *testing.T) {
	dir, head := buildTestPlanHistoryRepo(t, []string{"a", "b", "c"})
	s := NewPlanHistoryScreen()
	cmd := s.Open(dir, false, head)
	s.RecalculateLayout(80, 24)
	s, _ = s.Update(dispatch(t, cmd))

	// Click at row 1 inside list bounds → cursor moves to 1
	row := s.listBounds.Min.Y + 1
	click := tea.MouseClickMsg{X: 2, Y: row, Button: tea.MouseLeft}
	s, dc := s.HandleMouse(click)
	if s.cursor != 1 {
		t.Errorf("expected cursor=1 after list click; got %d", s.cursor)
	}
	if dc == nil {
		t.Error("expected detail load command after click")
	}

	// Click well outside both bounds → no-op
	prev := s.cursor
	out := tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft}
	s2, _ := s.HandleMouse(out)
	if s2.cursor != prev {
		t.Errorf("click outside bounds should not move cursor; got %d", s2.cursor)
	}
}

func TestPlanHistory_RelTimeFormatting(t *testing.T) {
	now := time.Now()
	cases := []struct {
		offset time.Duration
		want   string
	}{
		{30 * time.Second, "just now"},
		{2 * time.Minute, "2m ago"},
		{3 * time.Hour, "3h ago"},
	}
	for _, tc := range cases {
		got := relTime(now.Add(-tc.offset))
		if got != tc.want {
			t.Errorf("relTime(-%v) = %q, want %q", tc.offset, got, tc.want)
		}
	}
}
