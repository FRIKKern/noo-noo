package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FRIKKern/noo-noo/internal/config"
	"github.com/FRIKKern/noo-noo/internal/modules/offload"
	"github.com/FRIKKern/noo-noo/internal/store"
)

// TestPendingAutoApplyAllowedCascade pins the daemon-side gate cascade for
// auto-applying queued relocations (criterion: ONLY when auto_apply_pending
// AND risk_acknowledged_at are both set, and NEVER on pressure ticks).
func TestPendingAutoApplyAllowedCascade(t *testing.T) {
	armed := config.OffloadCfg{AutoApplyPending: true, RiskAcknowledgedAt: "2026-07-16T10:00:00Z"}
	cases := []struct {
		name    string
		trigger TickTrigger
		cfg     config.OffloadCfg
		want    bool
		reason  string
	}{
		{"pressure never applies even fully armed", TriggerPressure, armed, false, "trigger_not_daily"},
		{"manual IPC tick never auto-applies", TriggerManual, armed, false, "trigger_not_daily"},
		{"daily but master switch off (the default)", TriggerDaily, config.OffloadCfg{RiskAcknowledgedAt: "x"}, false, "auto_apply_pending_off"},
		{"daily, switch hand-flipped without acknowledgement", TriggerDaily, config.OffloadCfg{AutoApplyPending: true}, false, "risk_not_acknowledged"},
		{"daily fully armed", TriggerDaily, armed, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := pendingAutoApplyAllowed(tc.trigger, tc.cfg)
			if got != tc.want || reason != tc.reason {
				t.Fatalf("pendingAutoApplyAllowed(%v, %+v) = (%v, %q), want (%v, %q)",
					tc.trigger, tc.cfg, got, reason, tc.want, tc.reason)
			}
		})
	}

	// The compiled-in default is OFF: a fresh config must never auto-apply.
	if ok, reason := pendingAutoApplyAllowed(TriggerDaily, config.Defaults().Offload); ok {
		t.Fatalf("Defaults() must not arm auto-apply, got allowed (reason %q)", reason)
	}
}

// tickTestDaemon builds a Daemon whose RunTick is fast and hermetic: no scan
// roots, heuristics and notifications disabled, temp store.
func tickTestDaemon(t *testing.T, oc config.OffloadCfg) *Daemon {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Daemon.SocketPath = filepath.Join(dir, "noo.sock")
	cfg.Daemon.StorePath = filepath.Join(dir, "store.db")
	cfg.Notify.Enabled = false
	cfg.Heuristics.IdleRepos.Enabled = false
	cfg.Heuristics.CacheVelocity.Enabled = false
	cfg.Scan.Roots = nil
	cfg.Scan.CacheRoots = nil
	cfg.Offload = oc

	st, err := store.Open(cfg.Daemon.StorePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return newDaemon(cfg, st)
}

// TestRunTickPendingCascadeDispatch proves RunTick consults the cascade
// before the engine is ever invoked: the seam fires exactly on
// daily-and-armed, and never on pressure or unarmed ticks.
func TestRunTickPendingCascadeDispatch(t *testing.T) {
	orig := runPendingRelocFn
	t.Cleanup(func() { runPendingRelocFn = orig })

	armed := config.OffloadCfg{AutoApplyPending: true, RiskAcknowledgedAt: "2026-07-16T10:00:00Z"}
	cases := []struct {
		name     string
		trigger  TickTrigger
		cfg      config.OffloadCfg
		wantCall bool
	}{
		{"pressure + armed", TriggerPressure, armed, false},
		{"daily + default off", TriggerDaily, config.OffloadCfg{}, false},
		{"daily + switch without ack", TriggerDaily, config.OffloadCfg{AutoApplyPending: true}, false},
		{"daily + armed", TriggerDaily, armed, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := 0
			runPendingRelocFn = func(_ *Daemon, _ context.Context) ([]offload.PendingResult, error) {
				called++
				return nil, nil
			}
			d := tickTestDaemon(t, tc.cfg)
			if err := d.RunTick(context.Background(), tc.trigger); err != nil {
				t.Fatalf("RunTick: %v", err)
			}
			if (called > 0) != tc.wantCall {
				t.Fatalf("engine called %d times, wantCall=%v", called, tc.wantCall)
			}
		})
	}
}

// TestRunTickPressureLeavesQueueUntouched is the end-to-end pressure law
// WITHOUT any seam swap: a fully armed config, a ready queued row, a
// pressure tick through the real RunTick — the row must survive
// byte-for-byte and nothing on disk may move.
func TestRunTickPressureLeavesQueueUntouched(t *testing.T) {
	d := tickTestDaemon(t, config.OffloadCfg{
		DestRoot: "/Volumes/EXT/offload", DestVolumeUUID: "uuid-ext",
		AutoApplyPending: true, RiskAcknowledgedAt: "2026-07-16T10:00:00Z",
	})
	id, err := d.store.EnqueueRelocation(store.RelocationQueueEntry{
		QueuedAtUnix: 1000, TargetPath: "/home/u/.claude", PlaybookAssetID: "claude-config",
		DestRoot: "/Volumes/EXT/offload", DestVolumeUUID: "uuid-ext",
		GateReason: `stop-gate: "Claude" was running`,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if err := d.RunTick(context.Background(), TriggerPressure); err != nil {
		t.Fatalf("RunTick(pressure): %v", err)
	}

	row, err := d.store.GetRelocation(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.Status != store.RelocQueued || row.Attempts != 0 || row.LastAttemptAtUnix != 0 {
		t.Fatalf("pressure tick touched the queue row: %+v", row)
	}
}

// localGuard / localProcs / localCopy are minimal fakes for the end-to-end
// daily-tick test (offload's own test fakes are package-private).
type localGuard struct{}

func (localGuard) CheckDest(context.Context, string, string) error { return nil }

type localProcs struct{}

func (localProcs) Running(context.Context, string) (bool, error) { return false, nil }

func localCopy(_ context.Context, src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		f, err := os.Create(out)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, in); err != nil {
			_ = f.Close()
			return err
		}
		return f.Close()
	})
}

// TestRunTickDailyArmedAppliesAtMostOne drives the REAL engine through the
// tick seam with two ready queued rows: a fully armed daily tick applies
// exactly the oldest one (budget 1/tick) and leaves the second pending and
// untouched; the applied row is terminal with the two-phase trail.
func TestRunTickDailyArmedAppliesAtMostOne(t *testing.T) {
	home := t.TempDir()
	destRoot := filepath.Join(t.TempDir(), "offload")
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	d := tickTestDaemon(t, config.OffloadCfg{
		DestRoot: destRoot, DestVolumeUUID: "uuid-test",
		AutoApplyPending: true, RiskAcknowledgedAt: "2026-07-16T10:00:00Z",
	})

	var pbs []offload.Playbook
	assets := make([]string, 2)
	for i, name := range []string{"older", "newer"} {
		assets[i] = filepath.Join(home, name)
		if err := os.MkdirAll(assets[i], 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(assets[i], "f.bin"), []byte(strings.Repeat("x", 256)), 0o644); err != nil {
			t.Fatal(err)
		}
		pbs = append(pbs, offload.Playbook{AssetID: name, Path: "~/" + name, Class: offload.ClassRelocate})
	}

	var ids [2]int64
	for i, target := range assets {
		var err error
		ids[i], err = d.store.EnqueueRelocation(store.RelocationQueueEntry{
			QueuedAtUnix: int64(1000 + i), TargetPath: target,
			PlaybookAssetID: filepath.Base(target),
			DestRoot:        destRoot, DestVolumeUUID: "uuid-test",
			GateReason: "stop-gate: app",
		})
		if err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	// Same shape as the production seam, with hermetic deps instead of the
	// real pgrep/diskutil — budget 1 and the daily trigger stay identical.
	orig := runPendingRelocFn
	t.Cleanup(func() { runPendingRelocFn = orig })
	runPendingRelocFn = func(d *Daemon, ctx context.Context) ([]offload.PendingResult, error) {
		m := offload.New(offload.Config{
			DestRoot:       d.cfg.Offload.DestRoot,
			DestVolumeUUID: d.cfg.Offload.DestVolumeUUID,
		}, pbs, offload.Deps{Home: home, Guard: localGuard{}, Procs: localProcs{}, Copy: localCopy})
		return m.RunPending(ctx, d.store, offload.TriggerPendingDaily, 1, d.now)
	}

	if err := d.RunTick(context.Background(), TriggerDaily); err != nil {
		t.Fatalf("RunTick(daily): %v", err)
	}

	older, err := d.store.GetRelocation(ids[0])
	if err != nil {
		t.Fatalf("get older: %v", err)
	}
	if older.Status != store.RelocApplied || older.Attempts != 1 || older.FreedBytes <= 0 {
		t.Fatalf("oldest row not applied with two-phase trail: %+v", older)
	}
	if fi, err := os.Lstat(assets[0]); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("oldest asset not relocated to a symlink: %v %v", fi, err)
	}

	newer, err := d.store.GetRelocation(ids[1])
	if err != nil {
		t.Fatalf("get newer: %v", err)
	}
	if newer.Status != store.RelocQueued || newer.Attempts != 0 {
		t.Fatalf("budget of 1/tick violated — second row touched: %+v", newer)
	}
	if fi, err := os.Lstat(assets[1]); err != nil || !fi.IsDir() {
		t.Fatalf("second asset touched despite budget: %v %v", fi, err)
	}

	// A second daily tick drains the second entry — the queue empties over
	// successive ticks, one safe move at a time.
	if err := d.RunTick(context.Background(), TriggerDaily); err != nil {
		t.Fatalf("RunTick(daily) 2: %v", err)
	}
	newer, _ = d.store.GetRelocation(ids[1])
	if newer.Status != store.RelocApplied {
		t.Fatalf("second daily tick did not drain the queue: %+v", newer)
	}
}
