package ipc

import (
	"testing"
	"time"
)

// TestDaemonStatusReturnsUptime exercises Status directly (no wire) and
// checks the three fields the daemon main fills in: Running, Version, and
// Uptime derived from StartedAt. The 1-minute tolerance window absorbs
// scheduler jitter on slow CI without going so wide that a stuck clock
// would slip through.
func TestDaemonStatusReturnsUptime(t *testing.T) {
	started := time.Now().Add(-90 * time.Minute)
	svc := &DaemonService{
		StartedAt: func() time.Time { return started },
		Version:   "0.2.0",
	}
	var resp StatusResponse
	if err := svc.Status(StatusRequest{}, &resp); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !resp.Running {
		t.Errorf("Running = false, want true")
	}
	if resp.Version != "0.2.0" {
		t.Errorf("Version = %q, want 0.2.0", resp.Version)
	}
	if resp.Uptime < 89*time.Minute || resp.Uptime > 91*time.Minute {
		t.Errorf("Uptime out of range: %v", resp.Uptime)
	}
}

// TestDaemonStatusZeroStartedAt covers the boundary where StartedAt is nil
// (e.g. a service constructed without the daemon main wiring it up). The
// method must not panic; it should report Running with a zero Uptime so the
// CLI can still render something useful.
func TestDaemonStatusNilStartedAt(t *testing.T) {
	svc := &DaemonService{Version: "test"}
	var resp StatusResponse
	if err := svc.Status(StatusRequest{}, &resp); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !resp.Running {
		t.Errorf("Running = false, want true")
	}
	if resp.Uptime != 0 {
		t.Errorf("Uptime = %v, want 0 when StartedAt is nil", resp.Uptime)
	}
}
