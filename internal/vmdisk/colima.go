package vmdisk

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// dockerDataMount is the guest mount point of colima's datadisk — the
// filesystem that fills and kills dockerd. `df` rows are matched against it.
const dockerDataMount = "/var/lib/docker"

// runner returns the injected runner or the production exec-backed one.
func (d *Detector) runner() Runner {
	if d.Runner != nil {
		return d.Runner
	}
	return execRunner{}
}

// detectColima enumerates colima profiles via `colima list --json` and reads
// each running profile's inner datadisk usage. Parse/read errors on a single
// profile are non-fatal (the VM is reported without usage); only a hard
// runner failure of `colima list` itself aborts, and that too is swallowed to
// "no VMs" so a broken colima never breaks `noo-noo status`.
func (d *Detector) detectColima(ctx context.Context) ([]VM, error) {
	out, err := d.runner().Output(ctx, nil, "colima", "list", "--json")
	if err != nil {
		return nil, nil // colima present but unhappy → report no VMs, not an error
	}
	profiles := parseColimaList(out)
	vms := make([]VM, 0, len(profiles))
	for _, p := range profiles {
		vm := VM{
			Kind:              "colima",
			Name:              p.Name,
			Running:           strings.EqualFold(p.Status, "Running"),
			ConfiguredDiskGiB: int(p.Disk / gib),
			Home:              d.colimaHome(),
		}
		if vm.Running {
			vm.Datadisk = d.colimaDatadisk(ctx, p.Name)
		}
		vms = append(vms, vm)
	}
	return vms, nil
}

// colimaProfile is the subset of `colima list --json` we consume.
type colimaProfile struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Disk   uint64 `json:"disk"` // configured datadisk size in bytes
}

// parseColimaList tolerates both NDJSON (one object per line, colima's real
// output for multiple profiles) and a single JSON array.
func parseColimaList(out []byte) []colimaProfile {
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return nil
	}
	if out[0] == '[' {
		var arr []colimaProfile
		if err := json.Unmarshal(out, &arr); err == nil {
			return arr
		}
		return nil
	}
	var profiles []colimaProfile
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var p colimaProfile
		if err := json.Unmarshal(line, &p); err != nil {
			continue
		}
		if p.Name == "" {
			continue
		}
		profiles = append(profiles, p)
	}
	return profiles
}

// limaInstanceNameForColimaProfile maps a colima profile name to the LIMA
// instance name colima registers in its private _lima tree: "colima" for the
// default profile, "colima-<profile>" otherwise. This mapping is what makes
// the Detect dedup work when both binaries are installed.
func limaInstanceNameForColimaProfile(profile string) string {
	if profile == "" || profile == "default" {
		return "colima"
	}
	return "colima-" + profile
}

// colimaDatadisk reads inner usage via `colima ssh -p <profile> -- df -B1`.
func (d *Detector) colimaDatadisk(ctx context.Context, profile string) DiskUsage {
	args := []string{"ssh"}
	if profile != "" && profile != "default" {
		args = append(args, "-p", profile)
	}
	args = append(args, "--", "df", "-B1")
	out, err := d.runner().Output(ctx, nil, "colima", args...)
	if err != nil {
		return DiskUsage{}
	}
	return parseDatadiskDF(out)
}

// detectLima enumerates lima instances via `limactl list --json`, optionally
// under an override LIMA_HOME (colima's private $COLIMA_HOME/_lima tree). A
// nonexistent home is skipped silently.
func (d *Detector) detectLima(ctx context.Context, limaHome string) ([]VM, error) {
	if limaHome != "" {
		if _, err := os.Stat(limaHome); err != nil {
			return nil, nil // colima not installed / no private tree → nothing
		}
	}
	var env []string
	home := limaHome
	if limaHome == "" {
		home = defaultLimaHome() // record the concrete home for headroom checks
	} else {
		env = []string{"LIMA_HOME=" + limaHome}
	}
	out, err := d.runner().OutputEnv(ctx, env, "limactl", "list", "--json")
	if err != nil {
		return nil, nil
	}
	instances := parseLimaList(out)
	vms := make([]VM, 0, len(instances))
	for _, in := range instances {
		vm := VM{
			Kind:              "lima",
			Name:              in.Name,
			Running:           strings.EqualFold(in.Status, "Running"),
			ConfiguredDiskGiB: int(in.Disk / gib),
			Home:              home,
		}
		if vm.Running {
			vm.Datadisk = d.limaDatadisk(ctx, env, in.Name)
		}
		vms = append(vms, vm)
	}
	return vms, nil
}

type limaInstance struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Disk   uint64 `json:"disk"`
}

// parseLimaList tolerates NDJSON or a JSON array, like parseColimaList.
func parseLimaList(out []byte) []limaInstance {
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return nil
	}
	if out[0] == '[' {
		var arr []limaInstance
		if err := json.Unmarshal(out, &arr); err == nil {
			return arr
		}
		return nil
	}
	var instances []limaInstance
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var in limaInstance
		if err := json.Unmarshal(line, &in); err != nil {
			continue
		}
		if in.Name == "" {
			continue
		}
		instances = append(instances, in)
	}
	return instances
}

// limaDatadisk reads inner usage via `limactl shell <name> -- df -B1`.
func (d *Detector) limaDatadisk(ctx context.Context, env []string, name string) DiskUsage {
	out, err := d.runner().OutputEnv(ctx, env, "limactl", "shell", name, "--", "df", "-B1")
	if err != nil {
		return DiskUsage{}
	}
	return parseDatadiskDF(out)
}

// parseDatadiskDF parses `df -B1` output and returns the datadisk row —
// preferring the /var/lib/docker mount, else the largest /dev/vd* device
// (the additional disk lima attaches for docker data). Returns a zero-value
// (Present=false) DiskUsage when no plausible datadisk row is found.
//
// df -B1 columns: Filesystem 1B-blocks Used Available Use% Mounted-on
func parseDatadiskDF(out []byte) DiskUsage {
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var best DiskUsage   // largest /dev/vd* fallback
	var docker DiskUsage // exact /var/lib/docker match wins outright
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 6 {
			continue
		}
		if strings.EqualFold(fields[0], "Filesystem") {
			continue // header
		}
		fs := fields[0]
		total, err1 := strconv.ParseUint(fields[1], 10, 64)
		used, err2 := strconv.ParseUint(fields[2], 10, 64)
		mount := fields[len(fields)-1]
		if err1 != nil || err2 != nil || total == 0 {
			continue
		}
		if mount == dockerDataMount {
			docker = DiskUsage{Mount: mount, TotalBytes: total, UsedBytes: used, Present: true}
		}
		if strings.HasPrefix(fs, "/dev/vd") && total > best.TotalBytes {
			best = DiskUsage{Mount: mount, TotalBytes: total, UsedBytes: used, Present: true}
		}
	}
	if docker.Present {
		return docker
	}
	return best
}

// --- OS primitives (production defaults for the injectable seams) ---

// Runner runs one command and returns its stdout. It follows the core
// OutputRunner idiom but adds an env-scoped variant, because probing colima's
// private Lima tree requires running `limactl list` under a different
// LIMA_HOME — the env is load-bearing detection state, not a side channel, so
// it belongs in the seam tests inspect. Injectable so tests drive canned
// output and assert which LIMA_HOME was probed, never touching a real VM.
type Runner interface {
	// Output runs name+args with no extra environment.
	Output(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error)
	// OutputEnv runs name+args with extraEnv appended to the process env
	// (e.g. "LIMA_HOME=/Users/x/.colima/_lima").
	OutputEnv(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, error)
}

// execRunner is the production runner: os/exec with optional extra env.
type execRunner struct{}

func (execRunner) Output(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	return runExec(ctx, stdin, nil, name, args...)
}

func (execRunner) OutputEnv(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, error) {
	return runExec(ctx, nil, extraEnv, name, args...)
}

func runExec(ctx context.Context, stdin []byte, extraEnv []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func defaultLookPath(name string) (string, error) { return exec.LookPath(name) }

// defaultColimaHome resolves $COLIMA_HOME, else $HOME/.colima.
func defaultColimaHome() string {
	if h := os.Getenv("COLIMA_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".colima")
}

// defaultLimaHome resolves $LIMA_HOME, else $HOME/.lima.
func defaultLimaHome() string {
	if h := os.Getenv("LIMA_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".lima")
}

// statfsFreeBytes returns the free bytes available to an unprivileged user on
// the volume backing path (statfs Bavail * Bsize).
func statfsFreeBytes(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}

// mountPointOf returns the mount point of the volume backing path — the
// syscall the kernel answers directly, immune to /Volumes naming.
func mountPointOf(path string) (string, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return "", err
	}
	return unix.ByteSliceToString(st.Mntonname[:]), nil
}
