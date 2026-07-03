package orchestrator

import (
	"sync"
	"testing"
)

func TestRunUsage_ConcurrentRecordTotalUsed(t *testing.T) {
	u := NewRunUsage(0)

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
			u.TotalUsed()
		}
	}()

	wg.Wait()

	if got := u.TotalUsed(); got != 15000 {
		t.Errorf("TotalUsed() = %d, want 15000", got)
	}
}
