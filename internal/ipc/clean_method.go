package ipc

import (
	"fmt"
	"time"

	"github.com/FRIKKern/noo-noo/internal/store"
)

// cleanModule is the audit-log module label for IPC-driven cleanup. Keeping
// it as a package constant makes it greppable from the report renderer.
const cleanModule = "ipc.clean"

// Execute records one audit-log entry per target and returns a summary of
// bytes "freed". In Phase 0.2 the daemon does not perform the deletes itself
// (the user runs `noo-noo dev clean` from Phase 0.1); Execute records that
// the user accepted a suggestion. Phase 0.5 will introduce auto-clean and
// this method will then dispatch to the relevant heuristic module.
//
// The audit row's Outcome field distinguishes a real acceptance ("accepted")
// from a dry-run preview ("dry-run") so reports can filter accordingly.
func (c *CleanService) Execute(req CleanRequest, resp *CleanResponse) error {
	if c.Store == nil {
		return fmt.Errorf("clean: store not configured")
	}
	now := c.now()
	outcome := "accepted"
	if req.DryRun {
		outcome = "dry-run"
	}
	var total int64
	for _, t := range req.Targets {
		if t.Path == "" {
			return fmt.Errorf("clean: empty target path")
		}
		if _, err := c.Store.RecordAction(store.StoredAction{
			Ts:        now,
			Module:    cleanModule,
			Op:        "clean",
			Target:    t.Path,
			SizeBytes: t.Bytes,
			Outcome:   outcome,
		}); err != nil {
			return fmt.Errorf("audit insert: %w", err)
		}
		total += t.Bytes
	}
	*resp = CleanResponse{
		Targets:    len(req.Targets),
		BytesFreed: total,
	}
	return nil
}

// now returns the configured clock or time.Now if unset. Centralised so the
// nil-check is in one place.
func (c *CleanService) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
