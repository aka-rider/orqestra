package tui

import (
	"fmt"
	"strings"
	"testing"
)

func makeToolFrameActive(n int) *Frame {
	f := &Frame{Kind: AgentFrame, State: FrameInProgress, AgentID: "W"}
	for i := 1; i <= n; i++ {
		f.Parts = append(f.Parts, ContentPart{
			IsText: false,
			Tool:   ToolBlock{Name: fmt.Sprintf("tool_%d", i)},
		})
	}
	return f
}

func TestRenderFrameActive_ToolOverflow(t *testing.T) {
	f := makeToolFrameActive(20)
	out := renderFrameActive(f, 80, 0)

	if !containsStr(out, "⋯ +5 older tool calls") {
		t.Errorf("expected overflow indicator, got:\n%s", out)
	}
	if !containsStr(out, "tool_16") {
		t.Errorf("expected tool_16 (first visible), got:\n%s", out)
	}
	if containsStr(out, "tool_1 ") || strings.Contains(out, "tool_1\n") {
		lines := strings.Split(out, "\n")
		for _, l := range lines {
			if strings.Contains(l, "tool_1 ") || l == "tool_1" {
				t.Errorf("tool_1 should be hidden, but found line: %q", l)
			}
		}
	}
}

func TestRenderFrameActive_ToolOverflow_Expanded(t *testing.T) {
	f := makeToolFrameActive(20)
	f.ToolsExpanded = true
	out := renderFrameActive(f, 80, 0)

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

func TestRenderFrameActive_TextAlwaysVisible(t *testing.T) {
	f := &Frame{Kind: AgentFrame, State: FrameInProgress, AgentID: "W"}
	for i := 1; i <= 20; i++ {
		f.Parts = append(f.Parts, ContentPart{IsText: false, Tool: ToolBlock{Name: fmt.Sprintf("tool_%d", i)}})
		f.Parts = append(f.Parts, ContentPart{IsText: true, Text: fmt.Sprintf("text_after_%d\n", i)})
	}
	out := renderFrameActive(f, 80, 0)

	for i := 1; i <= 20; i++ {
		want := fmt.Sprintf("text_after_%d", i)
		if !containsStr(out, want) {
			t.Errorf("text part %q missing from output:\n%s", want, out)
		}
	}
}

func TestRenderFrameActive_NoOverflow(t *testing.T) {
	f := makeToolFrameActive(15)
	out := renderFrameActive(f, 80, 0)

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

	fl.ToggleFocusedTools()

	if fl.frames[0].ToolsExpanded {
		t.Fatal("ToggleFocusedTools with no focus must not mutate any frame")
	}
}
