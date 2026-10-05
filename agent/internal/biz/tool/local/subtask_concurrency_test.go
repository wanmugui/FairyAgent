package local

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// TestLocalSubtaskSessionFilesUniqueUnderConcurrency verifies that parallel
// create_subtask dispatches (each running in its own goroutine through the
// shared dispatcher) produce unique session/stream files, so concurrent
// subtasks never overwrite each other's state.
func TestLocalSubtaskSessionFilesUniqueUnderConcurrency(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "chat-x", "chat-x.json")
	var wg sync.WaitGroup
	seen := sync.Map{}
	var dup int32
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sf := localSubtaskSessionFile(parent, "并行任务")
			if _, loaded := seen.LoadOrStore(sf, true); loaded {
				atomic.AddInt32(&dup, 1)
			}
		}()
	}
	wg.Wait()
	if dup != 0 {
		t.Fatalf("%d duplicate subtask session files under concurrency", dup)
	}
}