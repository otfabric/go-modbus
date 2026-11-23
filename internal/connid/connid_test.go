// SPDX-License-Identifier: MIT

package connid

import (
	"context"
	"sync"
	"testing"
)

func TestNextUniqueAndNonZero(t *testing.T) {
	const workers, per = 8, 1000
	var mu sync.Mutex
	seen := make(map[uint64]bool, workers*per)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			ids := make([]uint64, per)
			for i := range ids {
				ids[i] = Next()
			}
			mu.Lock()
			defer mu.Unlock()
			for _, id := range ids {
				if id == 0 || seen[id] {
					t.Errorf("id %d is zero or repeated", id)
				}
				seen[id] = true
			}
		})
	}
	wg.Wait()
}

func TestWithFrom(t *testing.T) {
	if got := From(context.Background()); got != 0 {
		t.Fatalf("From(empty) = %d, want 0", got)
	}
	ctx := With(context.Background(), 42)
	if got := From(ctx); got != 42 {
		t.Fatalf("From = %d, want 42", got)
	}
}
