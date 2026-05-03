package ipc

import (
	"testing"
	"time"
)

type fakeScheduler struct {
	added int
	dur   time.Duration
	err   error
	calls int
}

func (f *fakeScheduler) TriggerNow() (int, time.Duration, error) {
	f.calls++
	return f.added, f.dur, f.err
}

func TestTriggerScan_Success(t *testing.T) {
	sched := &fakeScheduler{added: 2, dur: 500 * time.Millisecond}
	svc := &DaemonService{sched: sched}
	var reply TriggerScanReply
	if err := svc.TriggerScan(TriggerScanArgs{}, &reply); err != nil {
		t.Fatalf("TriggerScan: %v", err)
	}
	if !reply.Ok {
		t.Errorf("Ok = false, want true")
	}
	if reply.SuggestionsAdded != 2 {
		t.Errorf("SuggestionsAdded = %d, want 2", reply.SuggestionsAdded)
	}
	if reply.DurationMs != 500 {
		t.Errorf("DurationMs = %d, want 500", reply.DurationMs)
	}
	if sched.calls != 1 {
		t.Errorf("sched.calls = %d, want 1", sched.calls)
	}
}

func TestTriggerScan_PropagatesError(t *testing.T) {
	sched := &fakeScheduler{err: errSchedBusy}
	svc := &DaemonService{sched: sched}
	var reply TriggerScanReply
	err := svc.TriggerScan(TriggerScanArgs{}, &reply)
	if err == nil {
		t.Fatalf("expected error")
	}
	if reply.Ok {
		t.Errorf("Ok should be false on error")
	}
}

var errSchedBusy = &trivialErr{"busy"}

type trivialErr struct{ s string }

func (e *trivialErr) Error() string { return e.s }
