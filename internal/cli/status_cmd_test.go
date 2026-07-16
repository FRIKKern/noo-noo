package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/trend"
)

// fixtureStatus is a machine with real history: a filling internal disk, a
// usable big external, a Kompis-shaped hardware write-locked card, and an
// fs-corruption read-only volume.
func fixtureStatus() statusData {
	d := statusData{WindowDays: 30}
	d.Internal = internalPosture{
		MountPoint:    "/",
		TotalBytes:    228_000_000_000,
		FreeBytes:     15_000_000_000,
		Fit:           trend.Fit{OK: true, BytesPerDay: 1_200_000_000, WindowDays: 12, Points: 12},
		HistoryDays:   12,
		DaysUntilFull: 12.9,
		FullOK:        true,
	}
	d.Externals = []core.Volume{
		{UUID: "SAT-UUID", MountPoint: "/Volumes/SATECHI", TotalBytes: 1_800_000_000_000,
			FreeBytes: 1_700_000_000_000, WritableMedia: true, WritableVolume: true, Class: core.VolumeUsable},
		{UUID: "KOMPIS-UUID", MountPoint: "/Volumes/Kompis", TotalBytes: 32_000_000_000,
			FreeBytes: 16_000_000_000, WritableMedia: false, WritableVolume: false, Class: core.VolumeMediaRO},
		{UUID: "BROKEN-UUID", MountPoint: "/Volumes/Broken", TotalBytes: 500_000_000_000,
			FreeBytes: 100_000_000_000, WritableMedia: true, WritableVolume: false, Class: core.VolumeFSCorruptionRO},
	}
	d.Offload = offloadPosture{Configured: true, DestRoot: "/Volumes/SATECHI/noo-noo-offload",
		Usable: true, Detail: "verified writable just now (UUID-pinned, live write-probe)"}
	d.Pressure = pressurePosture{Batches24h: 180, NearContinuous: true}
	return d
}

// withStatusFixture swaps the gather hook for the test's fixture.
func withStatusFixture(t *testing.T, d statusData) {
	t.Helper()
	orig := statusGatherFn
	statusGatherFn = func(_ context.Context, _ string, _ int, _ time.Time) (statusData, error) {
		return d, nil
	}
	t.Cleanup(func() { statusGatherFn = orig })
}

func runStatus(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var out, errBuf bytes.Buffer
	app := &App{Out: &out, Err: &errBuf}
	code := app.Run(context.Background(), append([]string{"noo-noo", "status"}, args...))
	return out.String(), errBuf.String(), code
}

func TestStatusRendersPostureAndVerdicts(t *testing.T) {
	withStatusFixture(t, fixtureStatus())
	out, _, code := runStatus(t)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}

	for _, want := range []string{
		"212.3 GB total", "14.0 GB free", // core.Bytes rendering of the fixture
		"over the last 12 days", "full in ~13 days", // cited window + projection
		"/Volumes/SATECHI", "usable",
		"much of its day at the memory-pressure threshold", // D18 posture signal
		"usable external headroom",                         // verdict
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q\n---\n%s", want, out)
		}
	}

	// Kompis: hardware write-lock → rescue-copy, NEVER a repair suggestion.
	kompisLine := lineContaining(out, "/Volumes/Kompis")
	if kompisLine == "" {
		t.Fatalf("no Kompis line in output:\n%s", out)
	}
	if !strings.Contains(kompisLine, "HARDWARE WRITE-LOCKED") || !strings.Contains(kompisLine, "rescue-copy") {
		t.Errorf("Kompis verdict wrong: %q", kompisLine)
	}
	for _, banned := range []string{"repair", "First Aid", "fsck"} {
		if strings.Contains(strings.ToLower(kompisLine), strings.ToLower(banned)) {
			t.Errorf("Kompis (media-RO) line suggests %q — forbidden by charter D7: %q", banned, kompisLine)
		}
	}

	// fs-corruption RO: backup-then-repair IS the correct playbook.
	brokenLine := lineContaining(out, "/Volumes/Broken")
	if !strings.Contains(brokenLine, "back up") || !strings.Contains(brokenLine, "repair") {
		t.Errorf("fs-corruption verdict wrong: %q", brokenLine)
	}
}

func TestStatusInsufficientHistoryFallback(t *testing.T) {
	d := fixtureStatus()
	d.Internal.Fit = trend.Fit{OK: false, Points: 1}
	d.Internal.HistoryDays = 1
	d.CacheFallback = []cacheTrend{{
		Target: "/Users/x/Library/pnpm/store",
		Fit:    trend.Fit{OK: true, BytesPerDay: 150_000_000, WindowDays: 5, Points: 5},
	}}
	withStatusFixture(t, d)
	out, _, code := runStatus(t)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out, "insufficient history (1 day) — check back tomorrow") {
		t.Errorf("missing honest insufficient-history line:\n%s", out)
	}
	if !strings.Contains(out, "known grower (cache history): /Users/x/Library/pnpm/store") {
		t.Errorf("missing cache-velocity fallback:\n%s", out)
	}
}

func TestStatusStuckVerdict(t *testing.T) {
	d := fixtureStatus()
	// Only the write-locked and corrupt volumes remain: NO usable headroom.
	d.Externals = d.Externals[1:]
	d.Offload = offloadPosture{Configured: false, Detail: "not configured"}
	withStatusFixture(t, d)
	out, _, _ := runStatus(t)
	if !strings.Contains(out, "stuck on one small 212.3 GB disk") {
		t.Errorf("missing stuck verdict:\n%s", out)
	}
}

func TestStatusJSON(t *testing.T) {
	withStatusFixture(t, fixtureStatus())
	out, _, code := runStatus(t, "-json")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var j statusJSON
	if err := json.Unmarshal([]byte(out), &j); err != nil {
		t.Fatalf("status -json is not one JSON object: %v\n%s", err, out)
	}
	if j.Internal.TotalBytes != 228_000_000_000 || !j.Internal.FitOK {
		t.Errorf("internal JSON wrong: %+v", j.Internal)
	}
	if len(j.Externals) != 3 || j.Externals[1].Class != "media-ro" {
		t.Errorf("externals JSON wrong: %+v", j.Externals)
	}
	if !j.Pressure.NearContinuous {
		t.Errorf("pressure JSON wrong: %+v", j.Pressure)
	}
	if !strings.Contains(j.Verdict, "usable external headroom") {
		t.Errorf("verdict JSON wrong: %q", j.Verdict)
	}
}

// TestStatusVerbSafety: a positional argument must hard-error, never be
// silently swallowed (the `offload apply -y` trust bug, charter D22).
func TestStatusVerbSafety(t *testing.T) {
	withStatusFixture(t, fixtureStatus())
	cases := [][]string{
		{"full"},          // stray verb
		{"full", "-json"}, // flags AFTER a positional would be swallowed
		{"-days", "0"},    // out-of-range value
		{"-not-a-flag"},   // unknown flag
	}
	for _, args := range cases {
		_, errOut, code := runStatus(t, args...)
		if code != 2 {
			t.Errorf("status %v: exit = %d, want 2 (stderr: %s)", args, code, errOut)
		}
	}
}

func TestVerdictSentenceTable(t *testing.T) {
	usable := core.Volume{MountPoint: "/Volumes/S", FreeBytes: 1_700_000_000_000, Class: core.VolumeUsable}
	locked := core.Volume{MountPoint: "/Volumes/K", FreeBytes: 16_000_000_000, Class: core.VolumeMediaRO}
	cases := []struct {
		name      string
		externals []core.Volume
		off       offloadPosture
		want      string
	}{
		{"no volumes", nil, offloadPosture{}, "stuck on one small"},
		{"only unusable volumes", []core.Volume{locked}, offloadPosture{}, "stuck on one small"},
		{"headroom, offload healthy", []core.Volume{usable, locked},
			offloadPosture{Configured: true, Usable: true}, "offloading is cheaper than deleting"},
		{"headroom, offload unconfigured", []core.Volume{usable},
			offloadPosture{}, "configure [offload] to use it"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := verdictSentence(228_000_000_000, tc.externals, tc.off)
			if !strings.Contains(got, tc.want) {
				t.Errorf("verdict = %q, want it to contain %q", got, tc.want)
			}
		})
	}
	// The locked volume's free bytes must NOT count as headroom.
	got := verdictSentence(228_000_000_000, []core.Volume{usable, locked}, offloadPosture{})
	if !strings.Contains(got, "1.5 TB") { // 1.7e12 bytes = 1.5 TiB — Kompis' 16G excluded
		t.Errorf("headroom must sum only usable volumes: %q", got)
	}
}

// lineContaining returns the first output line containing substr.
func lineContaining(s, substr string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, substr) {
			return line
		}
	}
	return ""
}
