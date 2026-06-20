package orchestrator

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"bytes"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/harness"
)

// fixturePlayer is an Executor that replays a JSONL stream fixture through a
// Sink, converting stream-json lines to typed Events.
type fixturePlayer struct {
	path string
}

func (f *fixturePlayer) Run(_ context.Context, _ harness.ProcessSpec, in <-chan harness.Message, sink harness.Sink) (harness.RunResult, error) {
	// Drain the input plane in a goroutine — we don't inspect messages.
	if in != nil {
		go func() {
			for range in {
			}
		}()
	}

	file, err := os.Open(f.path)
	if err != nil {
		return harness.RunResult{}, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(line, &raw); err != nil {
			continue
		}
		typ, _ := unmarshalString(raw["type"])
		switch typ {
		case "assistant":
			emitAssistantEvents(raw["message"], sink)
		case "user":
			emitUserEvents(raw["message"], sink)
		case "result":
			if sink != nil {
				sink.Observe(harness.Event{Kind: harness.EventSessionDone})
			}
			return harness.RunResult{}, nil
		}
	}
	return harness.RunResult{}, scanner.Err()
}

func unmarshalString(raw json.RawMessage) (string, bool) {
	if raw == nil {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

func emitAssistantEvents(msgRaw json.RawMessage, sink harness.Sink) {
	if sink == nil || msgRaw == nil {
		return
	}
	var msg struct {
		Content []struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(msgRaw, &msg); err != nil {
		return
	}
	for _, block := range msg.Content {
		if block.Type == "tool_use" {
			var argsBuf bytes.Buffer
			if err := json.Compact(&argsBuf, block.Input); err == nil {
				sink.Observe(harness.Event{Kind: harness.EventToolUse, Tool: block.Name, Args: argsBuf.String()})
			}
		}
	}
}

func emitUserEvents(msgRaw json.RawMessage, sink harness.Sink) {
	if sink == nil || msgRaw == nil {
		return
	}
	var msg struct {
		Content []struct {
			Type    string `json:"type"`
			IsError bool   `json:"is_error"`
		} `json:"content"`
	}
	if err := json.Unmarshal(msgRaw, &msg); err != nil {
		return
	}
	for _, block := range msg.Content {
		if block.Type == "tool_result" {
			sink.Observe(harness.Event{Kind: harness.EventToolResult, IsError: block.IsError})
		}
	}
}

// TestLoopGuardFiresOnRealReplay replays the ExitWorktree loop fixture and
// verifies the LoopBreaker escalates before all 15 calls complete.
func TestLoopGuardFiresOnRealReplay(t *testing.T) {
	player := &fixturePlayer{path: "testdata/exitworktree_loop.jsonl"}
	exec := NewExecutorBuilder().With(NewLoopBreaker()).Wrap(player)

	spec := harness.ProcessSpec{
		Prompt: "explore the codebase",
		LoopGuard: harness.LoopGuardSpec{
			RepeatThreshold: 3,
			MaxNudges:       3,
			CooldownTurns:   2,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := exec.Run(ctx, spec, nil, nil)
	if !errors.Is(err, ErrLoopEscalated) {
		t.Errorf("expected ErrLoopEscalated on ExitWorktree loop replay, got: %v", err)
	}
}

// TestLoopGuardPassesNormalStream verifies that a non-looping stream completes
// without triggering escalation.
func TestLoopGuardPassesNormalStream(t *testing.T) {
	player := &fixturePlayer{path: "../harness/testdata/worker_stream_sample.jsonl"}
	exec := NewExecutorBuilder().With(NewLoopBreaker()).Wrap(player)

	spec := harness.ProcessSpec{
		Prompt: "do work",
		LoopGuard: harness.LoopGuardSpec{
			RepeatThreshold: 3,
			MaxNudges:       3,
			CooldownTurns:   2,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := exec.Run(ctx, spec, nil, nil)
	if err != nil {
		t.Errorf("expected no error on normal stream, got: %v", err)
	}
}
