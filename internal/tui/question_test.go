package tui

// Character-input shape for tests: bubbles/v2 textarea.Model.Update routes
// printable keys via msg.Text (bubbles/v2@v2.1.0/textarea/textarea.go:1316:
// insertRunesFromUserInput([]rune(msg.Text))). Therefore tests use
// tea.KeyPressMsg{Text: "x"} to type a character — matching the parent
// dispatch shape already used by TestUserQuestion_MultiSelectToggleVisible.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/mcp"
)

func qOptions() []mcp.ToolOption {
	return []mcp.ToolOption{
		{Label: "Alpha"},
		{Label: "Beta"},
		{Label: "Gamma"},
	}
}

func singleQ() mcp.ToolCall {
	return mcp.ToolCall{Question: "pick", Options: qOptions()}
}

func multiQ() mcp.ToolCall {
	return mcp.ToolCall{Question: "pick", MultiSelect: true, Options: qOptions()}
}

func freeformQ() mcp.ToolCall {
	return mcp.ToolCall{Question: "freeform"}
}

// ---------- Bug #1 (visual unification) ----------

func TestView_SingleAndMultiRenderIdenticallyWhenUnselected(t *testing.T) {
	single := newUserQuestion(singleQ(), 80)
	multi := newUserQuestion(multiQ(), 80)
	if single.View(80) != multi.View(80) {
		t.Errorf("single and multi views differ when unselected:\nsingle:\n%s\nmulti:\n%s",
			single.View(80), multi.View(80))
	}
}

func TestFooter_SingleVsMulti_DifferOnlyByToggle(t *testing.T) {
	single := newUserQuestion(singleQ(), 80)
	multi := newUserQuestion(multiQ(), 80)
	stripped := strings.Replace(multi.Footer(), " | [Space] toggle", "", 1)
	if stripped != single.Footer() {
		t.Errorf("expected multi footer minus toggle to equal single footer:\nmulti  : %q\nsingle : %q\nstripped: %q",
			multi.Footer(), single.Footer(), stripped)
	}
}

func TestMultiSelect_SpaceToggles(t *testing.T) {
	m := newUserQuestion(multiQ(), 80)
	out0 := m.View(80)
	m, _ = m.Update(tea.KeyPressMsg{Text: " "})
	out1 := m.View(80)
	if !strings.Contains(out1, "[x]") {
		t.Errorf("expected [x] after Space toggle, got:\n%s", out1)
	}
	if out0 == out1 {
		t.Errorf("expected view to change after toggle")
	}

	s := newUserQuestion(singleQ(), 80)
	before := s.View(80)
	s, _ = s.Update(tea.KeyPressMsg{Text: " "})
	after := s.View(80)
	if before != after {
		t.Errorf("expected single-select Space to be a no-op, got change")
	}
}

// ---------- Bug #3 (vanishing context + dynamic affordance) ----------

func TestTabHint_AddVsEdit(t *testing.T) {
	m := newUserQuestion(singleQ(), 80)
	v0 := m.View(80)
	if !strings.Contains(v0, "[Tab: add context]") {
		t.Errorf("expected '[Tab: add context]' on initial view, got:\n%s", v0)
	}
	if strings.Contains(v0, "[Tab: edit context]") {
		t.Errorf("did not expect '[Tab: edit context]' on initial view, got:\n%s", v0)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.activeEditor != 0 {
		t.Fatalf("expected activeEditor=0 after Tab, got %d", m.activeEditor)
	}
	for _, ch := range []string{"x", "x", "x"} {
		m, _ = m.Update(tea.KeyPressMsg{Text: ch})
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.activeEditor != -1 {
		t.Fatalf("expected activeEditor=-1 after Enter commit, got %d", m.activeEditor)
	}

	v1 := m.View(80)
	if !strings.Contains(v1, "✎ xxx") {
		t.Errorf("expected '✎ xxx' indicator, got:\n%s", v1)
	}
	if !strings.Contains(v1, "[Tab: edit context]") {
		t.Errorf("expected '[Tab: edit context]' after commit, got:\n%s", v1)
	}
	if strings.Contains(v1, "[Tab: add context]") {
		t.Errorf("did not expect '[Tab: add context]' after commit, got:\n%s", v1)
	}
}

func TestEnter_InsideEditor_DoesNotSubmitQuestion(t *testing.T) {
	m := newUserQuestion(singleQ(), 80)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = m.Update(tea.KeyPressMsg{Text: "y"})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Done() {
		t.Errorf("expected question not Done() after Enter inside editor")
	}
	if m.activeEditor != -1 {
		t.Errorf("expected activeEditor=-1 after Enter inside editor, got %d", m.activeEditor)
	}
}

func TestTab_ReopensEditorWithPriorValue(t *testing.T) {
	m := newUserQuestion(singleQ(), 80)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	for _, ch := range []string{"f", "o", "o"} {
		m, _ = m.Update(tea.KeyPressMsg{Text: ch})
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if got := m.ta.Value(); got != "foo" {
		t.Errorf("expected ta.Value()=\"foo\" after reopen, got %q", got)
	}
}

// ---------- Bug #2 (typing-lag) — rendering refresh through cached contentVP ----------

func newPipelineScreenWithFreeform(t *testing.T) PipelineScreen {
	t.Helper()
	s := NewPipelineScreen("test")
	s.content = ContentUserQuestion
	s.question = newUserQuestion(freeformQ(), 80)
	s.hasQuestion = true
	s.RecalculateLayout(80, 20)
	s.SyncViewports()
	return s
}

func TestKeystrokeRefreshesContentVPCache(t *testing.T) {
	s := newPipelineScreenWithFreeform(t)
	letters := []string{"a", "b", "c"}
	cumulative := ""
	for _, ch := range letters {
		var sNew PipelineScreen
		sNew, _ = s.Update(tea.KeyPressMsg{Text: ch})
		s = sNew
		cumulative += ch
		if !strings.Contains(s.contentVP.View(), cumulative) {
			t.Fatalf("after typing %q, expected contentVP.View() to contain %q, got:\n%s",
				ch, cumulative, s.contentVP.View())
		}
	}
}

func TestUpdateSubModel_RefreshesContentVPCache(t *testing.T) {
	s := newPipelineScreenWithFreeform(t)
	s, _ = s.UpdateSubModel(tea.KeyPressMsg{Text: "q"})
	if !strings.Contains(s.contentVP.View(), "q") {
		t.Errorf("expected contentVP.View() to contain 'q' after UpdateSubModel, got:\n%s",
			s.contentVP.View())
	}
}

func TestInlineEditor_KeystrokeRefreshesContentVPCache(t *testing.T) {
	s := NewPipelineScreen("test")
	s.content = ContentUserQuestion
	s.question = newUserQuestion(singleQ(), 80)
	s.hasQuestion = true
	s.RecalculateLayout(80, 20)
	s.SyncViewports()

	s, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	s, _ = s.Update(tea.KeyPressMsg{Text: "z"})
	if !strings.Contains(s.contentVP.View(), "z") {
		t.Errorf("expected contentVP.View() to contain 'z' after typing in inline editor, got:\n%s",
			s.contentVP.View())
	}
}

// ---------- Selection / confirmation ----------

func TestEnter_SingleSelect_ChoosesCursor(t *testing.T) {
	m := newUserQuestion(singleQ(), 80)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.Done() {
		t.Fatal("expected Done() after Enter")
	}
	got := m.Answer().SelectedIndices
	if len(got) != 1 || got[0] != 1 {
		t.Errorf("expected SelectedIndices=[1], got %v", got)
	}
}

func TestEnter_MultiSelect_UsesToggledSet(t *testing.T) {
	m := newUserQuestion(multiQ(), 80)
	m, _ = m.Update(tea.KeyPressMsg{Text: " "})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Text: " "})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	ans := m.Answer()
	if ans.Skipped {
		t.Fatalf("expected Skipped=false")
	}
	if got := ans.SelectedIndices; len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Errorf("expected SelectedIndices=[0,2], got %v", got)
	}
}

func TestEsc_ProducesSkipped(t *testing.T) {
	for _, q := range []mcp.ToolCall{singleQ(), multiQ(), freeformQ()} {
		m := newUserQuestion(q, 80)
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		if !m.Done() || !m.Answer().Skipped {
			t.Errorf("expected Done && Skipped for question %+v, got done=%v answer=%+v",
				q, m.Done(), m.Answer())
		}
	}
}

func TestCancel_ProducesSkipped(t *testing.T) {
	m := newUserQuestion(singleQ(), 80)
	m = m.Cancel()
	if !m.Done() || !m.Answer().Skipped {
		t.Errorf("expected Done && Skipped after Cancel, got done=%v answer=%+v",
			m.Done(), m.Answer())
	}
}

func TestFreeform_EnterSubmits_ShiftEnterNewlines(t *testing.T) {
	m := newUserQuestion(freeformQ(), 80)
	m, _ = m.Update(tea.KeyPressMsg{Text: "h"})
	m, _ = m.Update(tea.KeyPressMsg{Text: "i"})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	if m.Done() {
		t.Fatal("expected Shift+Enter NOT to submit freeform")
	}
	if !strings.Contains(m.ta.Value(), "\n") {
		t.Errorf("expected newline in freeform value after Shift+Enter, got %q", m.ta.Value())
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.Done() {
		t.Fatal("expected Done after plain Enter")
	}
	if !strings.Contains(m.Answer().FreeformText, "hi") {
		t.Errorf("expected freeform text to contain 'hi', got %q", m.Answer().FreeformText)
	}
}

func TestBuildAnswer_CustomTexts(t *testing.T) {
	m := newUserQuestion(multiQ(), 80)
	// Toggle option 0, add context "alpha"
	m, _ = m.Update(tea.KeyPressMsg{Text: " "})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	for _, ch := range []string{"a", "l", "p", "h", "a"} {
		m, _ = m.Update(tea.KeyPressMsg{Text: ch})
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	// Move to option 2, toggle, add context "beta"
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Text: " "})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	for _, ch := range []string{"b", "e", "t", "a"} {
		m, _ = m.Update(tea.KeyPressMsg{Text: ch})
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	// Confirm.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	ans := m.Answer()
	if got := ans.CustomTexts; got == nil || got[0] != "alpha" || got[2] != "beta" {
		t.Errorf("expected CustomTexts {0:alpha, 2:beta}, got %v", got)
	}
}
