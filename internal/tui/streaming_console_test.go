package tui

import (
	"strings"
	"testing"
)

func TestStreamingConsole_PartialThenComplete(t *testing.T) {
	c := newStreamingConsole(80)
	c, _ = c.Start()

	c = c.AppendDelta("Hel")
	c = c.AppendDelta("lo")

	view := c.View()
	if !strings.Contains(view, "Hello") {
		t.Errorf("expected partial 'Hello' in view, got %q", view)
	}

	// Complete the partial (promoted to transcript)
	c = c.CompletePartial()
	view2 := c.View()
	if strings.Contains(view2, "Hello") {
		t.Errorf("expected 'Hello' cleared after CompletePartial, got %q", view2)
	}
}

func TestStreamingConsole_ToolOK(t *testing.T) {
	c := newStreamingConsole(80)
	c, _ = c.Start()
	c = c.AddPendingTool("Read file.go")

	viewPending := c.View()
	if !strings.Contains(viewPending, "◌") {
		t.Errorf("expected ◌ for pending tool, got %q", viewPending)
	}

	c = c.ResolveLastTool(false)
	viewOK := c.View()
	if !strings.Contains(viewOK, "✓") {
		t.Errorf("expected ✓ for ok tool, got %q", viewOK)
	}
	if strings.Contains(viewOK, "◌") {
		t.Errorf("expected no ◌ after resolve, got %q", viewOK)
	}
}

func TestStreamingConsole_ToolErr(t *testing.T) {
	c := newStreamingConsole(80)
	c, _ = c.Start()
	c = c.AddPendingTool("Bash go test")
	c = c.ResolveLastTool(true) // is_error

	view := c.View()
	if !strings.Contains(view, "✗") {
		t.Errorf("expected ✗ for error tool, got %q", view)
	}
}

func TestStreamingConsole_BlinkToggles(t *testing.T) {
	c := newStreamingConsole(80)
	c, _ = c.Start()
	c = c.AppendDelta("working...")

	// Simulate first blink tick
	c1, _ := c.Update(streamBlinkMsg{tag: c.blinkTag})
	// Simulate second blink tick
	c2, _ := c1.Update(streamBlinkMsg{tag: c1.blinkTag})

	if c1.blinkOn == c.blinkOn {
		t.Error("first tick should toggle blinkOn")
	}
	if c2.blinkOn == c1.blinkOn {
		t.Error("second tick should toggle blinkOn back")
	}
}

func TestStreamingConsole_StaleBlinkTickIgnored(t *testing.T) {
	c := newStreamingConsole(80)
	c, _ = c.Start()

	savedTag := c.blinkTag
	c.blinkTag++ // advance tag — stale

	c2, cmd := c.Update(streamBlinkMsg{tag: savedTag})
	if c2.blinkOn != c.blinkOn {
		t.Error("stale blink tick must not toggle blinkOn")
	}
	if cmd != nil {
		t.Error("stale blink tick must not produce a cmd")
	}
}

func TestStreamingConsole_BlinkStableLineCount(t *testing.T) {
	c := newStreamingConsole(80)
	c, _ = c.Start()
	c = c.AddPendingTool("Read a.go")
	c = c.AppendDelta("thinking...")

	lines1 := strings.Count(c.View(), "\n")

	c2, _ := c.Update(streamBlinkMsg{tag: c.blinkTag})
	lines2 := strings.Count(c2.View(), "\n")

	if lines1 != lines2 {
		t.Errorf("blink changed line count: %d → %d", lines1, lines2)
	}
}

func TestStreamingConsole_DesiredHeight(t *testing.T) {
	c := newStreamingConsole(80)
	if c.DesiredHeight() != 0 {
		t.Error("inactive console should have DesiredHeight 0")
	}

	c, _ = c.Start()
	if c.DesiredHeight() != 1 { // just the indicator line
		t.Errorf("active with no tools: DesiredHeight = %d, want 1", c.DesiredHeight())
	}

	c = c.AddPendingTool("Read")
	if c.DesiredHeight() != 2 { // 1 tool + indicator
		t.Errorf("active with 1 tool: DesiredHeight = %d, want 2", c.DesiredHeight())
	}
}

func TestStreamingConsole_Reset(t *testing.T) {
	c := newStreamingConsole(80)
	c, _ = c.Start()
	c = c.AppendDelta("partial").AddPendingTool("Read")

	c = c.Reset()
	if c.active {
		t.Error("Reset should clear active")
	}
	if len(c.toolLines) != 0 {
		t.Error("Reset should clear toolLines")
	}
	if c.speechPartial != "" {
		t.Error("Reset should clear speechPartial")
	}
}
