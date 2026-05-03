package pressure

import (
	"context"
	"time"
)

// Threshold defines what counts as "high" pressure.
type Threshold struct {
	MemHighRatio   float64       // 0.0..1.0; sample is "high" if MemRatio >= this
	DiskLowGB      int           // sample is "high" if FreeDiskGB <= this
	SampleInterval time.Duration // how often to sample
	DebounceWindow time.Duration // how long to require sustained pressure
}

// triggerCooldown is the minimum time between consecutive onTrigger calls.
// Prevents tight-loop firing while pressure stays above threshold.
const triggerCooldown = 5 * time.Minute

// Sampler is the abstraction that vmstat + statfs implement.
type Sampler interface {
	Sample() (Reading, error)
}

// combinedSampler returns the higher-pressure of two samplers per call.
type combinedSampler struct {
	samplers []Sampler
}

func (c *combinedSampler) Sample() (Reading, error) {
	var out Reading
	out.FreeDiskGB = 1 << 30 // very high so min() works
	for _, s := range c.samplers {
		r, err := s.Sample()
		if err != nil {
			return Reading{}, err
		}
		if r.MemRatio > out.MemRatio {
			out.MemRatio = r.MemRatio
		}
		if r.FreeDiskGB < out.FreeDiskGB {
			out.FreeDiskGB = r.FreeDiskGB
		}
	}
	return out, nil
}

// Watch starts a sampling loop using the real vmstat+statfs samplers and
// calls onTrigger when sustained-high pressure is detected.
// Blocks until ctx is done.
func Watch(ctx context.Context, th Threshold, onTrigger func()) {
	s := &combinedSampler{
		samplers: []Sampler{NewVMStatSampler(), NewDiskSampler("/")},
	}
	WatchWithSampler(ctx, s, th, onTrigger)
}

// WatchWithSampler is the testable variant: callers inject a Sampler.
func WatchWithSampler(ctx context.Context, s Sampler, th Threshold, onTrigger func()) {
	bufLen := int(th.DebounceWindow / th.SampleInterval)
	if bufLen < 3 {
		bufLen = 3
	}
	buf := make([]bool, 0, bufLen)
	var lastFire time.Time

	ticker := time.NewTicker(th.SampleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r, err := s.Sample()
			if err != nil {
				continue
			}
			high := r.MemRatio >= th.MemHighRatio || r.FreeDiskGB <= float64(th.DiskLowGB)
			if len(buf) >= bufLen {
				buf = buf[1:]
			}
			buf = append(buf, high)
			if len(buf) < bufLen {
				continue // not enough data yet
			}
			highCount := 0
			for _, b := range buf {
				if b {
					highCount++
				}
			}
			frac := float64(highCount) / float64(len(buf))
			if frac >= 0.8 && time.Since(lastFire) > triggerCooldown {
				lastFire = time.Now()
				onTrigger()
			}
		}
	}
}
