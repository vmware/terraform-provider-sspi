package provider

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestPlatformLcmMuSerializesConcurrentActions verifies the concurrency
// guarantee platformLcmMu exists for: two goroutines racing to run a
// platform LCM action (modeled here as a critical section that increments a
// counter, holds the lock briefly, then decrements it) must never both be
// inside the critical section at the same time.
func TestPlatformLcmMuSerializesConcurrentActions(t *testing.T) {
	var concurrent int32
	var maxConcurrent int32
	var wg sync.WaitGroup

	criticalSection := func() {
		platformLcmMu.Lock()
		defer platformLcmMu.Unlock()

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
		t.Fatalf("expected platformLcmMu to serialize all critical sections (max concurrent = 1), observed max concurrent = %d", got)
	}
}
