package ipc

import (
	"errors"
	"time"
)

// SchedulerKicker is the subset of *daemon.Scheduler this method needs.
// Defining it here lets us test the IPC layer without importing the daemon
// (which would create an import cycle). The daemon main will satisfy this
// interface with its real scheduler when TriggerScan goes live in Phase 0.3.
type SchedulerKicker interface {
	TriggerNow() (added int, dur time.Duration, err error)
}

// TriggerScan executes one off-cycle scan and returns the count of new
// suggestions and the wall-clock duration. Concurrency (e.g. coalescing
// concurrent triggers, refusing to overlap with the daily tick) is the
// scheduler's responsibility; this method is a thin shell so the IPC
// surface stays uniform with Status, Report.Full, etc.
func (s *DaemonService) TriggerScan(_ TriggerScanArgs, reply *TriggerScanReply) error {
	if s.sched == nil {
		// net/rpc runs handlers on live goroutines: a panic here doesn't
		// fail one call, it kills the whole daemon (found the hard way —
		// every force-scan crashed noo-nood until this guard existed).
		reply.Ok = false
		return errors.New("trigger scan: no scheduler wired into this daemon build")
	}
	added, dur, err := s.sched.TriggerNow()
	if err != nil {
		reply.Ok = false
		return err
	}
	reply.Ok = true
	reply.SuggestionsAdded = added
	reply.DurationMs = dur.Milliseconds()
	return nil
}
