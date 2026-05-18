package orchestrator

import (
	"sync"
	"testing"
)

func TestRunUsage_RecordAndSnapshot(t *testing.T) {
	u := NewRunUsage(0)
	u.StartAgent("researcher", AgentMeta{ModelRef: "opus-4"})
	u.Record("researcher", 100, 50)
	u.Record("researcher", 200, 100)
	u.EndAgent("researcher", "done")

	u.StartAgent("architect", AgentMeta{ModelRef: "opus-4"})
	u.Record("architect", 300, 150)
	u.EndAgent("architect", "done")

	snap := u.Snapshot()
	if snap.Input != 600 {
		t.Errorf("snap.Input = %d, want 600", snap.Input)
	}
	if snap.Output != 300 {
		t.Errorf("snap.Output = %d, want 300", snap.Output)
	}
	if snap.Total() != 900 {
		t.Errorf("snap.Total() = %d, want 900", snap.Total())
	}
	if len(snap.Agents) != 2 {
		t.Fatalf("len(agents) = %d, want 2", len(snap.Agents))
	}

	r := snap.Agents[0]
	if r.AgentID != "researcher" {
		t.Errorf("agents[0].AgentID = %q, want researcher", r.AgentID)
	}
	if r.Input != 300 || r.Output != 150 {
		t.Errorf("researcher tokens = %d/%d, want 300/150", r.Input, r.Output)
	}
	if r.CallCount != 2 {
		t.Errorf("researcher.CallCount = %d, want 2", r.CallCount)
	}
	if r.Status != "done" {
		t.Errorf("researcher.Status = %q, want done", r.Status)
	}

	a := snap.Agents[1]
	if a.AgentID != "architect" || a.Input != 300 {
		t.Errorf("architect = %+v, unexpected", a)
	}
}

func TestRunUsage_AutoRegister(t *testing.T) {
	u := NewRunUsage(0)
	u.Record("worker", 500, 200)

	snap := u.Snapshot()
	if len(snap.Agents) != 1 {
		t.Fatalf("len(agents) = %d, want 1", len(snap.Agents))
	}
	if snap.Agents[0].Status != "running" {
		t.Errorf("auto-registered agent status = %q, want running", snap.Agents[0].Status)
	}
}

func TestRunUsage_ConcurrentRecordSnapshot(t *testing.T) {
	u := NewRunUsage(0)
	u.StartAgent("test", AgentMeta{})

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			u.Record("test", 10, 5)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			u.Snapshot()
		}
	}()

	wg.Wait()

	snap := u.Snapshot()
	if snap.Input != 10000 {
		t.Errorf("input = %d, want 10000", snap.Input)
	}
}

func TestRunSnapshot_BudgetPercent(t *testing.T) {
	tests := []struct {
		name  string
		snap  RunSnapshot
		want  float64
	}{
		{"unlimited", RunSnapshot{Input: 500, Output: 500, Limit: 0}, 0},
		{"50%", RunSnapshot{Input: 250, Output: 250, Limit: 1000}, 50},
		{"100%", RunSnapshot{Input: 500, Output: 500, Limit: 1000}, 100},
		{"over", RunSnapshot{Input: 800, Output: 800, Limit: 1000}, 160},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.snap.BudgetPercent()
			if got != tt.want {
				t.Errorf("BudgetPercent() = %f, want %f", got, tt.want)
			}
		})
	}
}

func TestRunSnapshot_AgentByID(t *testing.T) {
	snap := RunSnapshot{
		Agents: []AgentSnapshot{
			{AgentID: "researcher"},
			{AgentID: "architect"},
		},
	}

	a, ok := snap.AgentByID("architect")
	if !ok || a.AgentID != "architect" {
		t.Errorf("AgentByID(architect) = %v, %v", a, ok)
	}

	_, ok = snap.AgentByID("unknown")
	if ok {
		t.Error("AgentByID(unknown) should return false")
	}
}
