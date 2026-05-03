package ipc

import "time"

// Status returns daemon liveness information. The daemon main constructs
// DaemonService with StartedAt = (func() time.Time)(daemon.StartTime) and
// Version = build-stamped version string, so the response carries enough
// for `noo-noo daemon status` to render "running 1h23m, v0.2.0" without a
// follow-up call.
//
// A nil StartedAt is treated as "uptime unknown, but daemon is up": the
// method still returns Running=true so that callers using Status purely as
// a liveness ping (e.g. CLI wait-for-ready loops) keep working.
func (d *DaemonService) Status(_ StatusRequest, reply *StatusResponse) error {
	reply.Running = true
	reply.Version = d.Version
	if d.StartedAt != nil {
		reply.Uptime = time.Since(d.StartedAt())
	}
	return nil
}
