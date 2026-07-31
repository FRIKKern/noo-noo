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
	low := Reading{MemRatio: 0.3, FreeDiskGB: 100, DiskMeasured: true}
	high := Reading{MemRatio: 0.95, FreeDiskGB: 5, DiskMeasured: true}
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
	low := Reading{MemRatio: 0.3, FreeDiskGB: 100, DiskMeasured: true}
	high := Reading{MemRatio: 0.95, FreeDiskGB: 5, DiskMeasured: true}
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

// TestWatchIgnoresMemoryOnlyPressure pins the disk-only trigger law: a
// machine that chronically runs above the memory threshold but has plenty
// of disk must NEVER fire — measured live, the mem-OR-disk version produced
// 113 pressure scans in one day on exactly such a machine.
func TestWatchIgnoresMemoryOnlyPressure(t *testing.T) {
	memHot := Reading{MemRatio: 0.97, FreeDiskGB: 100, DiskMeasured: true}
	script := []Reading{memHot, memHot, memHot, memHot, memHot, memHot, memHot, memHot, memHot, memHot}
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
		t.Fatal("memory-only pressure fired a disk scan")
	}
}

// TestCombinedSamplerIgnoresUnmeasuredDisk pins the founding defect: a
// memory-only sampler's zero-value FreeDiskGB must not poison the merge —
// it once made every combined reading "0 GB free" and, under the disk-only
// trigger, fired hourly scans on a machine with 45 GB free.
func TestCombinedSamplerIgnoresUnmeasuredDisk(t *testing.T) {
	mem := &scriptSampler{vals: []Reading{{MemRatio: 0.97}}}
	disk := &scriptSampler{vals: []Reading{{FreeDiskGB: 45, DiskMeasured: true}}}
	c := &combinedSampler{samplers: []Sampler{mem, disk}}
	r, err := c.Sample()
	if err != nil {
		t.Fatal(err)
	}
	if !r.DiskMeasured || r.FreeDiskGB != 45 {
		t.Fatalf("merged reading = %+v, want measured 45 GB", r)
	}

	// A watch fed only unmeasured readings (broken disk sampler) must
	// never fire, however long the zero-value FreeDiskGB persists.
	unmeasured := Reading{MemRatio: 0.99}
	script := []Reading{unmeasured, unmeasured, unmeasured, unmeasured, unmeasured, unmeasured, unmeasured, unmeasured}
	var fired atomic.Int32
	th := Threshold{MemHighRatio: 0.85, DiskLowGB: 10, SampleInterval: 5 * time.Millisecond, DebounceWindow: 30 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	go WatchWithSampler(ctx, &scriptSampler{vals: script}, th, func() { fired.Add(1) })
	time.Sleep(80 * time.Millisecond)
	if fired.Load() != 0 {
		t.Fatal("unmeasured-disk readings fired a scan")
	}
}
