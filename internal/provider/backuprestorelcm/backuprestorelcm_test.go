// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package backuprestorelcm

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestMuSerializesConcurrentActions verifies the concurrency guarantee Mu
// exists for: two goroutines racing to run a critical section (modeling
// sspi_backup's and sspi_restore's Create()) must never both be inside it
// at the same time, since both resources lock this same, shared instance.
func TestMuSerializesConcurrentActions(t *testing.T) {
	var concurrent int32
	var maxConcurrent int32
	var wg sync.WaitGroup

	criticalSection := func() {
		Mu.Lock()
		defer Mu.Unlock()

		n := atomic.AddInt32(&concurrent, 1)
		for {
			max := atomic.LoadInt32(&maxConcurrent)
			if n <= max || atomic.CompareAndSwapInt32(&maxConcurrent, max, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&concurrent, -1)
	}

	const goroutines = 5
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			criticalSection()
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&maxConcurrent); got != 1 {
		t.Fatalf("expected Mu to serialize all critical sections (max concurrent = 1), observed max concurrent = %d", got)
	}
}
