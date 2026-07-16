package cli

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/config"
	"github.com/FRIKKern/noo-noo/internal/modules/offload"
	"github.com/FRIKKern/noo-noo/internal/store"
)

// --- fakes (offload's own test fakes are package-private) -------------------

type okGuard struct{}

func (okGuard) CheckDest(context.Context, string, string) error { return nil }

type toggleProcs struct{ running bool }

func (p *toggleProcs) Running(context.Context, string) (bool, error) { return p.running, nil }

func cliGoCopy(_ context.Context, src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
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
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o644)
	})
}

// offloadCLIFixture wires the seams to a hermetic module + store: a temp
// home with one stop-gated relocate asset, a temp queue store, and a
// scripted stdin. Returns the toggleable process gate and the asset path.
func offloadCLIFixture(t *testing.T) (procs *toggleProcs, asset, destRoot string, qs func() *store.Store) {
	t.Helper()
	home := t.TempDir()
	asset = filepath.Join(home, "asset")
	if err := os.MkdirAll(asset, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(asset, "f.bin"), []byte(strings.Repeat("x", 300)), 0o644); err != nil {
		t.Fatal(err)
	}
	destRoot = filepath.Join(t.TempDir(), "offload")
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(t.TempDir(), "store.db")

	procs = &toggleProcs{running: true}
	pbs := []offload.Playbook{{
		AssetID: "asset", Path: "~/asset", Class: offload.ClassRelocate, StopGate: "BlockingApp",
	}}

	cfg := config.Defaults()
	cfg.Daemon.StorePath = storePath
	cfg.Offload = config.OffloadCfg{DestRoot: destRoot, DestVolumeUUID: "uuid-test"}

	origSetup, origStdin := offloadSetup, offloadStdin
	t.Cleanup(func() { offloadSetup, offloadStdin = origSetup, origStdin })
	offloadSetup = func() (config.Config, *offload.Module) {
		return cfg, offload.New(offload.Config{DestRoot: destRoot, DestVolumeUUID: "uuid-test"},
			pbs, offload.Deps{Home: home, Guard: okGuard{}, Procs: procs, Copy: cliGoCopy})
	}

	qs = func() *store.Store {
		s, err := store.Open(storePath)
		if err != nil {
			t.Fatalf("open queue store: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	return procs, asset, destRoot, qs
}

func runOffload(t *testing.T, stdin string, args ...string) (out, errOut string, code int) {
	t.Helper()
	offloadStdin = strings.NewReader(stdin)
	var o, e bytes.Buffer
	app := &App{Out: &o, Err: &e}
	code = offloadCmd(context.Background(), app, args)
	return o.String(), e.String(), code
}

// TestOffloadApplyDeferFlagAfterVerb is the b8 trust law for THIS slice:
// `offload apply -y --defer` — flags AFTER the verb — must actually take
// effect (queue without prompting), never silently parse into nothing.
func TestOffloadApplyDeferFlagAfterVerb(t *testing.T) {
	_, asset, _, qs := offloadCLIFixture(t)

	out, errOut, code := runOffload(t, "", "apply", "-y", "--defer")
	if code != 0 {
		t.Fatalf("exit %d (out %q, err %q)", code, out, errOut)
	}
	if !strings.Contains(out, "queued (id ") {
		t.Fatalf("--defer after verb did not queue; out: %q err: %q", out, errOut)
	}
	// Consent artifact: exactly one queued row for the asset.
	pending, err := qs().ListPendingRelocations()
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %v (err %v), want 1", pending, err)
	}
	if pending[0].TargetPath != asset || pending[0].Status != store.RelocQueued {
		t.Fatalf("queued row wrong: %+v", pending[0])
	}
	if !strings.Contains(pending[0].GateReason, "stop-gate") {
		t.Fatalf("gate reason lost: %+v", pending[0])
	}
	// Source untouched (still gated).
	if fi, err := os.Lstat(asset); err != nil || !fi.IsDir() {
		t.Fatalf("source touched by a deferred apply: %v %v", fi, err)
	}
}

// TestOffloadApplyPromptConsent: without --defer the stop-gate refusal
// OFFERS queueing; "y" queues, "n" (and EOF) leaves the queue empty and
// exits non-zero — a refusal is never silently swallowed.
func TestOffloadApplyPromptConsent(t *testing.T) {
	t.Run("yes queues", func(t *testing.T) {
		_, _, _, qs := offloadCLIFixture(t)
		out, _, code := runOffload(t, "y\n", "apply", "-y")
		if code != 0 || !strings.Contains(out, "queued (id ") {
			t.Fatalf("consented queue failed: exit %d out %q", code, out)
		}
		if pending, _ := qs().ListPendingRelocations(); len(pending) != 1 {
			t.Fatalf("want 1 queued row, got %v", pending)
		}
	})
	t.Run("no declines", func(t *testing.T) {
		_, _, _, qs := offloadCLIFixture(t)
		out, _, code := runOffload(t, "n\n", "apply", "-y")
		if code != 1 {
			t.Fatalf("declined gate refusal must exit 1, got %d (out %q)", code, out)
		}
		if !strings.Contains(out, "Not queued") {
			t.Fatalf("decline must say so; out %q", out)
		}
		if pending, _ := qs().ListPendingRelocations(); len(pending) != 0 {
			t.Fatalf("decline still queued: %v", pending)
		}
	})
}

// TestOffloadFlagsBeforeVerbHardError: the pre-D22 calling convention is a
// hard usage error now — never a silent reinterpretation.
func TestOffloadFlagsBeforeVerbHardError(t *testing.T) {
	for _, args := range [][]string{{"-y", "apply"}, {"--json", "scan"}, {}} {
		_, errOut, code := runOffload(t, "", args...)
		if code != 2 || !strings.Contains(errOut, "Usage: noo-noo offload") {
			t.Fatalf("args %v: want usage error exit 2, got %d (err %q)", args, code, errOut)
		}
	}
}

// TestOffloadPendingListsAgeAndGateReason: the queue is visible — id,
// target, status, age, the deferring gate, and the latest re-check verdict.
func TestOffloadPendingListsAgeAndGateReason(t *testing.T) {
	_, asset, _, qs := offloadCLIFixture(t)
	s := qs()
	id, err := s.EnqueueRelocation(store.RelocationQueueEntry{
		QueuedAtUnix: time.Now().Add(-73 * time.Hour).Unix(), // 3 days ago
		TargetPath:   asset, PlaybookAssetID: "asset",
		DestRoot: "ignored", DestVolumeUUID: "ignored",
		GateReason: `stop-gate: "BlockingApp" was running`,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := s.ResolveRelocation(id, store.RelocationResolution{
		Status: store.RelocBlocked, BlockedReason: `stop-gate: "BlockingApp" still running`,
	}); err != nil {
		t.Fatalf("resolve blocked: %v", err)
	}

	out, _, code := runOffload(t, "", "pending")
	if code != 0 {
		t.Fatalf("pending exit %d (out %q)", code, out)
	}
	for _, want := range []string{
		"#" + strconv.FormatInt(id, 10), asset, "blocked", "queued 3d ago",
		`deferred because: stop-gate: "BlockingApp" was running`,
		`last re-check: stop-gate: "BlockingApp" still running`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("pending output missing %q:\n%s", want, out)
		}
	}

	// Honest empty state.
	if err := s.ResolveRelocation(id, store.RelocationResolution{Status: store.RelocCancelled, ResolvedAtUnix: time.Now().Unix()}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	out, _, _ = runOffload(t, "", "pending")
	if !strings.Contains(out, "No pending relocations") {
		t.Fatalf("empty state missing:\n%s", out)
	}
}

// TestOffloadRunPendingBlockedThenApplies: run-pending marks a still-gated
// entry blocked (exit 1, source untouched); once the app quits, the same
// command applies it (symlink live, row terminal).
func TestOffloadRunPendingBlockedThenApplies(t *testing.T) {
	procs, asset, destRoot, qs := offloadCLIFixture(t)

	if _, _, code := runOffload(t, "", "apply", "-y", "--defer"); code != 0 {
		t.Fatal("fixture queueing failed")
	}

	// Still gated.
	out, _, code := runOffload(t, "", "run-pending")
	if code != 1 || !strings.Contains(out, "still blocked") || !strings.Contains(out, "stop-gate") {
		t.Fatalf("gated run-pending: exit %d out %q", code, out)
	}
	if fi, err := os.Lstat(asset); err != nil || !fi.IsDir() {
		t.Fatalf("source touched while blocked: %v %v", fi, err)
	}

	// The app quits → the queued move applies.
	procs.running = false
	out, _, code = runOffload(t, "", "run-pending")
	if code != 0 || !strings.Contains(out, "relocated: "+asset) {
		t.Fatalf("cleared run-pending: exit %d out %q", code, out)
	}
	if fi, err := os.Lstat(asset); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("asset not relocated to symlink: %v %v", fi, err)
	}
	if resolved, _ := filepath.EvalSymlinks(asset); resolved == "" || !strings.HasPrefix(resolved, mustEval(t, destRoot)) {
		t.Fatalf("symlink resolves outside destRoot: %q", resolved)
	}
	if pending, _ := qs().ListPendingRelocations(); len(pending) != 0 {
		t.Fatalf("queue not drained: %v", pending)
	}
}

// TestOffloadCancelWithdrawsConsent: cancel is terminal; the entry leaves
// the pending list and run-pending no longer touches it.
func TestOffloadCancelWithdrawsConsent(t *testing.T) {
	_, _, _, qs := offloadCLIFixture(t)
	if _, _, code := runOffload(t, "", "apply", "-y", "--defer"); code != 0 {
		t.Fatal("fixture queueing failed")
	}
	pending, _ := qs().ListPendingRelocations()
	if len(pending) != 1 {
		t.Fatalf("fixture: %v", pending)
	}
	id := strconv.FormatInt(pending[0].ID, 10)

	out, _, code := runOffload(t, "", "cancel", id)
	if code != 0 || !strings.Contains(out, "cancelled #"+id) {
		t.Fatalf("cancel: exit %d out %q", code, out)
	}
	if pending, _ := qs().ListPendingRelocations(); len(pending) != 0 {
		t.Fatalf("cancelled row still pending: %v", pending)
	}

	// Cancelling again (terminal) fails loudly, and bad ids are usage errors.
	if _, errOut, code := runOffload(t, "", "cancel", id); code != 1 || !strings.Contains(errOut, "already resolved") {
		t.Fatalf("double-cancel: exit %d err %q", code, errOut)
	}
	if _, _, code := runOffload(t, "", "cancel", "not-a-number"); code != 2 {
		t.Fatalf("bad id must be usage error, got %d", code)
	}
	if _, _, code := runOffload(t, "", "cancel"); code != 2 {
		t.Fatalf("missing id must be usage error, got %d", code)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", p, err)
	}
	return r
}
