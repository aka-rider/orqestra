package tui

import (
	"fmt"
	"strings"
	"testing"
)

func makeToolFrame(n int) *Frame {
	f := &Frame{Kind: AgentFrame, State: FrameFinished, AgentID: "W"}
	for i := 1; i <= n; i++ {
		f.Parts = append(f.Parts, ContentPart{
			IsText: false,
			Tool:   ToolBlock{Name: fmt.Sprintf("tool_%d", i)},
		})
	}
	return f
}

func TestRenderFrame_ToolOverflow(t *testing.T) {
	f := makeToolFrame(20)
	out := renderFrameBody(f, 80)

	if !containsStr(out, "⋯ +5 older tool calls") {
		t.Errorf("expected overflow indicator, got:\n%s", out)
	}
	if !containsStr(out, "tool_16") {
		t.Errorf("expected tool_16 (first visible), got:\n%s", out)
	}
	if containsStr(out, "tool_1 ") || strings.Contains(out, "tool_1\n") {
		// tool_1 should be hidden; check it doesn't appear as a standalone tool label
		// (the string "tool_1" may appear as a prefix of "tool_10", so check carefully)
		lines := strings.Split(out, "\n")
		for _, l := range lines {
			if strings.Contains(l, "tool_1 ") || l == "tool_1" {
				t.Errorf("tool_1 should be hidden, but found line: %q", l)
			}
		}
	}
}

func TestRenderFrame_ToolOverflow_Expanded(t *testing.T) {
	f := makeToolFrame(20)
	f.ToolsExpanded = true
	out := renderFrameBody(f, 80)

	if containsStr(out, "⋯") {
		t.Errorf("expanded frame should not show overflow indicator, got:\n%s", out)
	}
	for i := 1; i <= 20; i++ {
		name := fmt.Sprintf("tool_%d", i)
		if !containsStr(out, name) {
			t.Errorf("expanded frame missing %s, got:\n%s", name, out)
		}
	}
}

func TestRenderFrame_TextAlwaysVisible(t *testing.T) {
	f := &Frame{Kind: AgentFrame, State: FrameFinished, AgentID: "W"}
	for i := 1; i <= 20; i++ {
		f.Parts = append(f.Parts, ContentPart{IsText: false, Tool: ToolBlock{Name: fmt.Sprintf("tool_%d", i)}})
		f.Parts = append(f.Parts, ContentPart{IsText: true, Text: fmt.Sprintf("text_after_%d\n", i)})
	}
	out := renderFrameBody(f, 80)

	// All text parts must appear regardless of tool overflow state.
	for i := 1; i <= 20; i++ {
		want := fmt.Sprintf("text_after_%d", i)
		if !containsStr(out, want) {
			t.Errorf("text part %q missing from output:\n%s", want, out)
		}
	}
}

func TestRenderFrame_NoOverflow(t *testing.T) {
	f := makeToolFrame(15)
	out := renderFrameBody(f, 80)

	if containsStr(out, "⋯") {
		t.Errorf("15-tool frame should not show overflow indicator, got:\n%s", out)
	}
	for i := 1; i <= 15; i++ {
		name := fmt.Sprintf("tool_%d", i)
		if !containsStr(out, name) {
			t.Errorf("tool %s missing from 15-tool frame, got:\n%s", name, out)
		}
	}
}

func TestFrameList_ToggleFocusedTools(t *testing.T) {
	fl := NewFrameList(80)
	fl.AppendFrame(Frame{Kind: AgentFrame, State: FrameFinished, AgentID: "W"})
	fl.FocusFrame(0)

	fl.ToggleFocusedTools()
	if !fl.frames[0].ToolsExpanded {
		t.Fatal("expected ToolsExpanded=true after first toggle")
	}
	if !fl.IsDirty() {
		t.Fatal("expected dirty=true after toggle")
	}

	// Reset dirty by rendering
	fl.Render()

	fl.ToggleFocusedTools()
	if fl.frames[0].ToolsExpanded {
		t.Fatal("expected ToolsExpanded=false after second toggle")
	}
	if !fl.IsDirty() {
		t.Fatal("expected dirty=true after second toggle")
	}
}

func TestFrameList_ToggleFocusedTools_NoFocus(t *testing.T) {
	fl := NewFrameList(80)
	fl.AppendFrame(Frame{Kind: AgentFrame, State: FrameFinished, AgentID: "W"})
	// focused == -1 (no FocusFrame call)

	fl.ToggleFocusedTools() // must not panic

	if fl.frames[0].ToolsExpanded {
		t.Fatal("ToggleFocusedTools with no focus must not mutate any frame")
	}
}
