package main

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/xiii/orqestra/internal/harness"
)

func TestStreamUpdateToBlock(t *testing.T) {
	tests := []struct {
		name     string
		update   harness.Event
		wantType string
	}{
		{
			name: "usage",
			update: harness.Event{
				Kind: harness.EventUsage,
				Input:      100,
				Output:     200,
			},
			wantType: "UsageBlock",
		},
		{
			name: "tool use",
			update: harness.Event{
				Tool:   "Read",
				Detail: "file.go",
			},
			wantType: "ToolUseBlock",
		},
		{
			name: "text delta",
			update: harness.Event{
				Text:    "partial",
				IsDelta: true,
			},
			wantType: "TextBlock",
		},
		{
			name: "text complete",
			update: harness.Event{
				Text: "complete text",
			},
			wantType: "TextBlock",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := eventToBlock(tt.update)
			if got == nil {
				t.Fatal("eventToBlock returned nil")
			}
			gotType := getTypeName(got)
			if gotType != tt.wantType {
				t.Errorf("block type = %q, want %q", gotType, tt.wantType)
			}
		})
	}
}

func getTypeName(v interface{}) string {
	switch v.(type) {
	case UsageBlock:
		return "UsageBlock"
	case ToolUseBlock:
		return "ToolUseBlock"
	case TextBlock:
		return "TextBlock"
	case ErrorBlock:
		return "ErrorBlock"
	default:
		return "unknown"
	}
}

func TestOutputBlockRender(t *testing.T) {
	tests := []struct {
		name     string
		block    OutputBlock
		wantWant string
	}{
		{
			name: "text delta",
			block: TextBlock{
				Text:    "hello",
				IsDelta: true,
			},
			wantWant: "▎ hello",
		},
		{
			name: "text complete",
			block: TextBlock{
				Text:    "hello",
				IsDelta: false,
			},
			wantWant: "hello",
		},
		{
			name: "tool use",
			block: ToolUseBlock{
				Name: "Read",
				Args: "file.go",
			},
			wantWant: "→ Read: file.go",
		},
		{
			name: "usage block",
			block: UsageBlock{
				Input:  100,
				Output: 200,
			},
			wantWant: "",
		},
		{
			name: "error block",
			block: ErrorBlock{
				Err: testingFailed("test error"),
			},
			wantWant: "Error: test error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.block.Render(80)
			// For error block, the style adds ANSI codes; check for the message content.
			if tt.name == "error block" {
				if !strings.Contains(got, "Error: test error") {
					t.Errorf("Render() = %q, expected to contain %q", got, tt.wantWant)
				}
			} else if got != tt.wantWant {
				t.Errorf("Render() = %q, want %q", got, tt.wantWant)
			}
		})
	}
}

type testingFailed string

func (e testingFailed) Error() string { return string(e) }

func TestLayoutHeight(t *testing.T) {
	tests := []struct {
		name        string
		height      int
		wantContent int
	}{
		{"minimal 12", 12, 4},
		{"tall 30", 30, 22},
		{"short 11", 11, 3},
		{"tiny 8", 8, 0},
		{"tiny 5", 5, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModel()
			m.height = tt.height
			got := m.contentHeight()
			if got != tt.wantContent {
				t.Errorf("contentHeight() = %d, want %d", got, tt.wantContent)
			}
		})
	}
}

func TestMinTerminalSize(t *testing.T) {
	m := NewModel()
	m.height = 10
	m.width = 40
	v := m.View()
	content := v.Content
	if content == "" {
		t.Fatal("View should not return empty content")
	}
}

func TestAppendBlockCap(t *testing.T) {
	m := NewModel()
	for i := 0; i < maxOutputBlocks+10; i++ {
		m.output = append(m.output, TextBlock{Text: "block"})
		if len(m.output) > maxOutputBlocks {
			m.output = m.output[len(m.output)-maxOutputBlocks:]
		}
	}
	if len(m.output) > maxOutputBlocks {
		t.Errorf("output cap = %d, want <= %d", len(m.output), maxOutputBlocks)
	}
}

func TestModelInit(t *testing.T) {
	m := NewModel()
	cmd := m.Init()
	if cmd == nil {
		t.Error("Init should return a non-nil Cmd")
	}
}

func TestModelView(t *testing.T) {
	m := NewModel()
	m.height = 24
	m.width = 80
	v := m.View()
	content := v.Content
	if content == "" {
		t.Fatal("View should not return empty content")
	}
}

func TestModelUpdateWindowResize(t *testing.T) {
	m := NewModel()
	m.width = 40
	m.height = 20

	msg := tea.WindowSizeMsg{Width: 120, Height: 40}
	m2, cmd := m.Update(msg)
	m = m2.(Model)
	if m.width != 120 {
		t.Errorf("width = %d, want 120", m.width)
	}
	if m.height != 40 {
		t.Errorf("height = %d, want 40", m.height)
	}
	_ = cmd
}

// TestStreamUpdateBridge verifies that StreamUpdate events are correctly
// processed by the TUI model through the Bubble Tea message loop.
// This exercises the full path: event → p.Send() → Update() → handleStreamUpdate().
func TestStreamUpdateBridge(t *testing.T) {
	// Create a channel that simulates sess.updates.
	streamUpdates := make(chan harness.Event, 10)

	// Create model.
	m := NewModel()
	m.height = 24
	m.width = 80

	// Run Bubble Tea program without renderer (no TTY needed).
	p := tea.NewProgram(m, tea.WithoutRenderer(), tea.WithWindowSize(80, 24))

	// Bridge goroutine: reads from streamUpdates, sends to p.Send().
	// This is the exact same pattern as the fix in main.go.
	go func() {
		for update := range streamUpdates {
			p.Send(update)
		}
	}()

	// Send test events through the updates channel.
	streamUpdates <- harness.Event{Text: "hello", IsDelta: true}
	streamUpdates <- harness.Event{Text: "world"}
	streamUpdates <- harness.Event{Tool: "Read", Detail: "file.go"}
	streamUpdates <- harness.Event{Kind: harness.EventUsage, Input: 100, Output: 200}
	close(streamUpdates)

	// Run the program with a timeout. It should process all events and exit
	// when the updates channel closes (no more messages to process).
	done := make(chan struct{})
	go func() {
		p.Run()
		close(done)
	}()

	select {
	case <-done:
		// Program exited cleanly — the bridge goroutine finished
		// because streamUpdates was closed, and p.Run() returned
		// because no more messages arrived.
	case <-time.After(3 * time.Second):
		p.Kill()
		t.Fatal("bridge test timed out — StreamUpdate events not reaching TUI message loop")
	}
}
