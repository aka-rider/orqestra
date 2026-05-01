package scheduler
package scheduler

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiii/orqestra/internal/types"
)

func TestDAGExecutionOrder(t *testing.T) {
	// A depends on B: B runs first, then A
	graph := ExecutionGraph{
		Agents: []AgentNode{
			{Role: "A", DependsOn: []string{"B"}},
			{Role: "B", DependsOn: nil},
		},
		Concurrency: 0,
	}

	s, err := New(graph)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	var mu sync.Mutex
	var order []string

	runner := func(ctx context.Context, node AgentNode, spec types.Specification) error {
		mu.Lock()
		order = append(order, node.Role)
		mu.Unlock()
		return nil
	}

	err = s.Run(context.Background(), types.Specification{}, runner, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if len(order) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(order))
	}
	if order[0] != "B" || order[1] != "A" {
		t.Errorf("expected order [B, A], got %v", order)
	}
}

func TestCycleDetection(t *testing.T) {
	graph := ExecutionGraph{
		Agents: []AgentNode{
			{Role: "A", DependsOn: []string{"B"}},
			{Role: "B", DependsOn: []string{"A"}},
		},
	}

	_, err := New(graph)
	if err == nil {
		t.Fatal("expected error for cyclic graph, got nil")
	}
}

func TestSerialExecution(t *testing.T) {
	graph := ExecutionGraph{
		Agents: []AgentNode{
			{Role: "A", DependsOn: nil},
			{Role: "B", DependsOn: nil},
			{Role: "C", DependsOn: nil},
		},
		Concurrency: 1,
	}

	s, err := New(graph)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	var running atomic.Int32
	var maxConcurrent atomic.Int32

	runner := func(ctx context.Context, node AgentNode, spec types.Specification) error {
		cur := running.Add(1)
		// Track max concurrency
		for {
			old := maxConcurrent.Load()
			if cur <= old {
				break
			}
			if maxConcurrent.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		running.Add(-1)
		return nil
	}

	err = s.Run(context.Background(), types.Specification{}, runner, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if maxConcurrent.Load() != 1 {
		t.Errorf("expected max concurrency 1, got %d", maxConcurrent.Load())
	}
}

func TestUnlimitedParallel(t *testing.T) {
	graph := ExecutionGraph{
		Agents: []AgentNode{
			{Role: "A", DependsOn: nil},
			{Role: "B", DependsOn: nil},
			{Role: "C", DependsOn: nil},
		},
		Concurrency: 0,
	}

	s, err := New(graph)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	var running atomic.Int32
	var maxConcurrent atomic.Int32

	runner := func(ctx context.Context, node AgentNode, spec types.Specification) error {
		cur := running.Add(1)
		for {
			old := maxConcurrent.Load()
			if cur <= old {
				break
			}
			if maxConcurrent.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		running.Add(-1)
		return nil
	}

	err = s.Run(context.Background(), types.Specification{}, runner, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// With concurrency=0 and no deps, all 3 should run simultaneously
	if maxConcurrent.Load() < 2 {
		t.Errorf("expected max concurrency >= 2 for parallel execution, got %d", maxConcurrent.Load())
	}
}

func TestAgentFailureDoesNotCrash(t *testing.T) {
	graph := ExecutionGraph{
		Agents: []AgentNode{
			{Role: "A", DependsOn: nil},
			{Role: "B", DependsOn: []string{"A"}},
		},
		Concurrency: 0,
	}

	s, err := New(graph)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	runner := func(ctx context.Context, node AgentNode, spec types.Specification) error {
		if node.Role == "A" {
			return errors.New("agent A failed")
		}
		return nil
	}

	var events []Event
	notify := func(e Event) {
		events = append(events, e)
	}

	err = s.Run(context.Background(), types.Specification{}, runner, notify)
	if err != nil {
		t.Fatalf("Run() should not return error for agent failure: %v", err)
	}

	// B should not have run (dependency failed)
	for _, e := range events {
		if e.Role == "B" && e.Type == EventAgentStarted {
			t.Error("agent B should not have started when dependency A failed")
		}
	}
}

func TestEmptyGraph(t *testing.T) {
	graph := ExecutionGraph{
		Agents:      nil,
		Concurrency: 0,
	}

	s, err := New(graph)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	err = s.Run(context.Background(), types.Specification{}, func(ctx context.Context, node AgentNode, spec types.Specification) error {
		t.Fatal("runner should not be called for empty graph")
		return nil
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
}

func TestSingleAgentNoDeps(t *testing.T) {
	graph := ExecutionGraph{
		Agents: []AgentNode{
			{Role: "solo", DependsOn: nil, Validator: &ValidatorNode{Role: "solo-val", ModelRef: "m"}},
		},
		Concurrency: 0,
	}

	s, err := New(graph)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	var events []Event
	runner := func(ctx context.Context, node AgentNode, spec types.Specification) error {
		return nil
	}

	err = s.Run(context.Background(), types.Specification{}, runner, func(e Event) {
		events = append(events, e)
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Should see: started, validation_started, validation_passed, done
	expectedTypes := []EventType{EventAgentStarted, EventValidationStarted, EventValidationPassed, EventAgentDone}
	if len(events) != len(expectedTypes) {
		t.Fatalf("expected %d events, got %d: %+v", len(expectedTypes), len(events), events)
	}
	for i, et := range expectedTypes {
		if events[i].Type != et {
			t.Errorf("event[%d]: expected type %d, got %d", i, et, events[i].Type)
		}
	}
}
