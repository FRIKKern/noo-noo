//go:build e2e

// Package e2e is the home of long-running, machinery-heavy integration tests
// that exercise full daemon paths (pressure watcher, autoclean engine, etc.).
// Gated behind the `e2e` build tag so `go test ./...` stays fast for the
// default loop; CI runs `go test -tags=e2e ./test/e2e/...` separately.
package e2e

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/pressure"
)

// scriptedSampler returns a fixed sequence of Readings (one per Sample call)
// then sticks on the last one. Lets us script a low-then-high-then-sustained
// pressure curve in a deterministic, fast test.
type scriptedSampler struct {
	values []pressure.Reading
	i      int
}

func (s *scriptedSampler) Sample() (pressure.Reading, error) {
	if s.i >= len(s.values) {
		return s.values[len(s.values)-1], nil
	}
	v := s.values[s.i]
	s.i++
	return v, nil
}

// TestPressureScan asserts that a sustained-high pressure stream causes the
// pressure.Watch loop to fire onTrigger within 60 s of real wall time. The
// test uses a 100 ms sample interval and a 6 s debounce window so the
// expected fire happens in ~5-7 s, well under the 60 s budget the verifier
// enforces (`go test -tags=e2e -run TestPressureScan`).
func TestPressureScan(t *testing.T) {
	low := pressure.Reading{MemRatio: 0.4, FreeDiskGB: 80}
	high := pressure.Reading{MemRatio: 0.92, FreeDiskGB: 6}
	script := []pressure.Reading{low, low, low}
	for i := 0; i < 200; i++ {
		script = append(script, high)
	}
	sampler := &scriptedSampler{values: script}

	th := pressure.Threshold{
		MemHighRatio:   0.85,
		DiskLowGB:      10,
		SampleInterval: 100 * time.Millisecond,
		DebounceWindow: 6 * time.Second,
	}

	var triggered atomic.Int32
	var triggeredAt atomic.Int64
	onTrigger := func() {
		if triggered.CompareAndSwap(0, 1) {
			triggeredAt.Store(time.Now().UnixMilli())
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	started := time.Now().UnixMilli()
	go pressure.WatchWithSampler(ctx, sampler, th, onTrigger)

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if triggered.Load() == 1 {
			elapsed := time.Duration(triggeredAt.Load()-started) * time.Millisecond
			if elapsed > 60*time.Second {
				t.Fatalf("trigger fired but took %v (> 60 s budget)", elapsed)
			}
			t.Logf("pressure trigger fired in %v", elapsed)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("pressure trigger did not fire within 60 s")
}
