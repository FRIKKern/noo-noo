package pressure

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type scriptSampler struct {
	vals []Reading
	i    int
}

func (s *scriptSampler) Sample() (Reading, error) {
	if s.i >= len(s.vals) {
		return s.vals[len(s.vals)-1], nil
	}
	v := s.vals[s.i]
	s.i++
	return v, nil
}

func TestWatchTriggersOnSustainedHigh(t *testing.T) {
	low := Reading{MemRatio: 0.3, FreeDiskGB: 100}
	high := Reading{MemRatio: 0.95, FreeDiskGB: 5}
	script := []Reading{low, low, high, high, high, high, high, high, high, high}
	s := &scriptSampler{vals: script}
	var fired atomic.Int32
	th := Threshold{
		MemHighRatio:   0.85,
		DiskLowGB:      10,
		SampleInterval: 5 * time.Millisecond,
		DebounceWindow: 30 * time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	go WatchWithSampler(ctx, s, th, func() { fired.Add(1) })
	time.Sleep(150 * time.Millisecond)
	if fired.Load() == 0 {
		t.Fatal("onTrigger never fired despite sustained high")
	}
}

func TestWatchDoesNotTriggerOnTransientSpike(t *testing.T) {
	low := Reading{MemRatio: 0.3, FreeDiskGB: 100}
	high := Reading{MemRatio: 0.95, FreeDiskGB: 5}
	// single spike then back to normal
	script := []Reading{low, low, high, low, low, low, low, low, low, low}
	s := &scriptSampler{vals: script}
	var fired atomic.Int32
	th := Threshold{
		MemHighRatio:   0.85,
		DiskLowGB:      10,
		SampleInterval: 5 * time.Millisecond,
		DebounceWindow: 30 * time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	go WatchWithSampler(ctx, s, th, func() { fired.Add(1) })
	time.Sleep(80 * time.Millisecond)
	if fired.Load() != 0 {
		t.Fatal("single spike should not trigger")
	}
}
