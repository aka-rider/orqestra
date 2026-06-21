package orchestrator

import (
	"sync"
	"testing"
)

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
