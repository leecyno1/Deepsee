package media

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRefreshXORCoalescesConcurrentScans(t *testing.T) {
	keys := NewKeys()
	var scans atomic.Int32
	scanStarted := make(chan struct{})
	releaseScan := make(chan struct{})
	keys.scan = func(string) (byte, error) {
		if scans.Add(1) == 1 {
			close(scanStarted)
		}
		<-releaseScan
		return 0x5a, nil
	}

	const callers = 12
	start := make(chan struct{})
	results := make(chan byte, callers)
	var ready sync.WaitGroup
	ready.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			ready.Done()
			<-start
			key, err := keys.RefreshXOR("/tmp/media-fixture")
			if err != nil {
				t.Errorf("RefreshXOR: %v", err)
				return
			}
			results <- key
		}()
	}
	ready.Wait()
	close(start)
	<-scanStarted
	time.Sleep(20 * time.Millisecond)
	close(releaseScan)

	for i := 0; i < callers; i++ {
		if key := <-results; key != 0x5a {
			t.Fatalf("result key = 0x%x, want 0x5a", key)
		}
	}
	if got := scans.Load(); got != 1 {
		t.Fatalf("scan calls = %d, want 1", got)
	}
}
