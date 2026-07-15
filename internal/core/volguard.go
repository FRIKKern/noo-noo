package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Volume-guard errors. Callers match with errors.Is to distinguish the
// failure modes; every one of them means "do NOT write to this destination".
var (
	// ErrDestUnconfigured: offload has no destination configured.
	ErrDestUnconfigured = errors.New("offload destination not configured")
	// ErrVolumeAbsent: the destination path does not exist or diskutil
	// cannot resolve it to a mounted volume.
	ErrVolumeAbsent = errors.New("destination volume absent")
	// ErrUUIDMismatch: a volume is mounted at the path but it is not the
	// pinned one. Mount-point names are NOT identity — a different disk can
	// mount under the same /Volumes name.
	ErrUUIDMismatch = errors.New("destination volume UUID mismatch")
	// ErrNotWritable: diskutil reports the volume read-only.
	ErrNotWritable = errors.New("destination volume not writable")
	// ErrProbeFailed: the live write-probe failed. Presence is not
	// usability — a mounted, parseable volume can still be read-only from
	// filesystem corruption (verified counterexample: /Volumes/Kompis).
	ErrProbeFailed = errors.New("destination write-probe failed")
)

// OutputRunner runs one command with optional stdin and returns its stdout.
// Injectable so tests can fake diskutil/plutil without touching real disks.
type OutputRunner interface {
	Output(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error)
}

// ExecOutputRunner is the production OutputRunner backed by os/exec.
type ExecOutputRunner struct{}

func (ExecOutputRunner) Output(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w (stderr: %s)", name, err, strings.TrimSpace(errBuf.String()))
	}
	return out.Bytes(), nil
}

// VolGuard verifies that an offload destination is the exact volume the user
// pinned AND is live-writable right now. Identity is the volume UUID (never
// the mount-point name); usability is proven by an actual write.
type VolGuard struct {
	Runner OutputRunner // nil → ExecOutputRunner{}
}

// volumeInfo is the subset of `diskutil info -plist` we care about, after
// plutil converts the plist to JSON. The writable flags are pointers so a
// missing key is distinguishable from an explicit false.
type volumeInfo struct {
	VolumeUUID     string `json:"VolumeUUID"`
	MountPoint     string `json:"MountPoint"`
	Writable       *bool  `json:"Writable"`
	WritableVolume *bool  `json:"WritableVolume"`
}

// CheckDest returns nil only when destDir sits on the volume whose UUID
// equals pinnedUUID and a live write-probe inside destDir succeeds.
//
// The chain is: stat destDir → `diskutil info -plist destDir` piped through
// `plutil -convert json -o - -` (no plist dependency) → UUID equality →
// writable flags → create+remove a probe file. Every step that can lie is
// backed by the step after it; only the live write is trusted as final.
func (g VolGuard) CheckDest(ctx context.Context, destDir, pinnedUUID string) error {
	if destDir == "" || pinnedUUID == "" {
		return fmt.Errorf("%w: set [offload] dest_root and dest_volume_uuid", ErrDestUnconfigured)
	}
	runner := g.Runner
	if runner == nil {
		runner = ExecOutputRunner{}
	}

	if info, err := os.Stat(destDir); err != nil || !info.IsDir() {
		return fmt.Errorf("%w: %q is not a reachable directory", ErrVolumeAbsent, destDir)
	}

	plist, err := runner.Output(ctx, nil, "/usr/sbin/diskutil", "info", "-plist", destDir)
	if err != nil {
		return fmt.Errorf("%w: diskutil cannot resolve %q: %v", ErrVolumeAbsent, destDir, err)
	}
	jsonBytes, err := runner.Output(ctx, plist, "/usr/bin/plutil", "-convert", "json", "-o", "-", "-")
	if err != nil {
		return fmt.Errorf("%w: plutil conversion failed for %q: %v", ErrVolumeAbsent, destDir, err)
	}
	var vi volumeInfo
	if err := json.Unmarshal(jsonBytes, &vi); err != nil {
		return fmt.Errorf("%w: unparseable diskutil output for %q: %v", ErrVolumeAbsent, destDir, err)
	}

	if !strings.EqualFold(vi.VolumeUUID, pinnedUUID) {
		return fmt.Errorf("%w: mounted volume at %q has UUID %s, pinned %s",
			ErrUUIDMismatch, destDir, vi.VolumeUUID, pinnedUUID)
	}
	if (vi.Writable != nil && !*vi.Writable) || (vi.WritableVolume != nil && !*vi.WritableVolume) {
		return fmt.Errorf("%w: diskutil reports %q read-only", ErrNotWritable, destDir)
	}

	// Live probe: diskutil flags can be stale or wrong (Kompis mounted with
	// Writable metadata yet every write failed "Read-only file system").
	// Only an actual write proves the destination.
	probe, err := os.CreateTemp(destDir, ".noo-noo-probe-*")
	if err != nil {
		return fmt.Errorf("%w: create probe in %q: %v", ErrProbeFailed, destDir, err)
	}
	name := probe.Name()
	if _, err := probe.Write([]byte("noo-noo volume probe\n")); err != nil {
		_ = probe.Close()
		_ = os.Remove(name)
		return fmt.Errorf("%w: write probe %q: %v", ErrProbeFailed, name, err)
	}
	if err := probe.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("%w: close probe %q: %v", ErrProbeFailed, name, err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("%w: remove probe %q: %v", ErrProbeFailed, name, err)
	}
	return nil
}
