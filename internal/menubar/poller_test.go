package menubar

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClient struct {
	calls atomic.Int32
	err   error
}

func (f *fakeClient) Status() (Status, error) {
	f.calls.Add(1)
	if f.err != nil {
		return Status{}, f.err
	}
	return Status{Running: true, OpenSuggestions: 2}, nil
}

func TestPoller_FiresImmediatelyAndOnInterval(t *testing.T) {
	c := &fakeClient{}
	got := make(chan Status, 8)
	p := NewPoller(c, 20*time.Millisecond, func(s Status) { got <- s })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	go p.Run(ctx)

	// Expect first poll < 5 ms; then ~3 more inside 100 ms.
	select {
	case <-got:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("no initial poll")
	}
	<-ctx.Done()
	if c.calls.Load() < 3 {
		t.Errorf("expected >=3 polls in 100 ms with 20 ms interval, got %d", c.calls.Load())
	}
}

func TestPoller_BacksOffOnError(t *testing.T) {
	c := &fakeClient{err: errors.New("dial: refused")}
	p := NewPoller(c, 10*time.Millisecond, func(Status) {})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	go p.Run(ctx)
	<-ctx.Done()
	// First call immediate; backoff doubles 10 -> 20 -> 40, so <= 4 calls in 60 ms.
	if c.calls.Load() > 4 {
		t.Errorf("backoff failed: %d calls in 60 ms (expected <= 4)", c.calls.Load())
	}
}
