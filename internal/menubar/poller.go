package menubar

import (
	"context"
	"time"
)

// StatusClient is the subset of ipc.Client the poller depends on.
type StatusClient interface {
	Status() (Status, error)
}

// Poller calls Status() on its client at a base interval, doubling the
// interval on consecutive errors (cap 5 m), resetting on success.
type Poller struct {
	client   StatusClient
	base     time.Duration
	cap      time.Duration
	onUpdate func(Status)
}

// NewPoller wires a poller. base is the success-state interval; the backoff
// cap defaults to 5 minutes.
func NewPoller(c StatusClient, base time.Duration, onUpdate func(Status)) *Poller {
	return &Poller{client: c, base: base, cap: 5 * time.Minute, onUpdate: onUpdate}
}

// Run blocks until ctx is cancelled. The first poll fires immediately.
func (p *Poller) Run(ctx context.Context) {
	interval := p.base
	for {
		st, err := p.client.Status()
		if err == nil {
			p.onUpdate(st)
			interval = p.base
		} else {
			interval *= 2
			if interval > p.cap {
				interval = p.cap
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
