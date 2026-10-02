// Memory posture: physical RAM, swap, the compressor's share, and the
// processes holding the most compressed memory — read live from the three
// macOS tools that know (sysctl, vm_stat, top), each tolerated separately.
//
// Disk was never the whole story on the founding machine: the day the
// socket vanished, load was 46, swap was 8 GB of 8, the compressor held
// 3 GB of an 8 GB machine and Jetsam was killing processes — and nothing in
// noo-noo could say so. This is the shared read; `status` renders the
// one-line verdict and `report` can render the fuller section.
package core

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// MemoryVerdict is the honest one-word posture.
type MemoryVerdict string

const (
	// MemoryFine: no meaningful swap pressure.
	MemoryFine MemoryVerdict = "fine"
	// MemoryTight: real swap in use or a large compressor — close to paging.
	MemoryTight MemoryVerdict = "tight"
	// MemoryThrashing: swap full or the compressor dominating RAM while
	// swap is deep — the machine is paging, not computing.
	MemoryThrashing MemoryVerdict = "thrashing"
)

// MemoryProcess is one row of `top -o mem -stats pid,command,mem,cmprs`.
type MemoryProcess struct {
	PID             int
	Command         string
	ResidentBytes   int64
	CompressedBytes int64
}

// MemorySnapshot is one live reading. Zero fields mean "could not read";
// Notes says why.
type MemorySnapshot struct {
	PhysicalBytes  int64 // hw.memsize
	SwapUsedBytes  int64 // vm.swapusage used
	SwapTotalBytes int64 // vm.swapusage total (grows on demand; 0 = no swap)
	// CompressorBytes is RAM occupied by the compressor (vm_stat "Pages
	// occupied by compressor" x page size) — the share of physical memory
	// that is holding squeezed pages instead of working pages.
	CompressorBytes int64
	// CompressedBytes is the logical size stored in the compressor (vm_stat
	// "Pages stored in compressor" x page size).
	CompressedBytes int64
	// TopCompressed is the top N processes by compressed memory; empty when
	// top failed (see Notes).
	TopCompressed []MemoryProcess
	// Notes lists tolerated read failures, human-phrased.
	Notes []string
}

// CompressorShare is CompressorBytes / PhysicalBytes (0 when unknown).
func (m MemorySnapshot) CompressorShare() float64 {
	if m.PhysicalBytes <= 0 {
		return 0
	}
	return float64(m.CompressorBytes) / float64(m.PhysicalBytes)
}

// SwapFill is SwapUsedBytes / SwapTotalBytes (0 when there is no swap).
func (m MemorySnapshot) SwapFill() float64 {
	if m.SwapTotalBytes <= 0 {
		return 0
	}
	return float64(m.SwapUsedBytes) / float64(m.SwapTotalBytes)
}

// swapOfRAM is swap used relative to physical RAM — the number that says
// how much of the working set no longer fits.
func (m MemorySnapshot) swapOfRAM() float64 {
	if m.PhysicalBytes <= 0 {
		return 0
	}
	return float64(m.SwapUsedBytes) / float64(m.PhysicalBytes)
}

// Verdict thresholds. Tuned against the founding 8 GB machine: a quiet day
// shows < 1 GB swap and a compressor under 1 GB; the Jetsam day showed swap
// 8/8 (100% of RAM) and a 3 GB compressor (37%).
const (
	memTightSwapOfRAM      = 0.25 // swap used >= a quarter of RAM
	memTightCompressor     = 0.25 // compressor >= a quarter of RAM
	memThrashSwapOfRAM     = 0.50 // AND one of the two below:
	memThrashSwapFill      = 0.90 //   swap file(s) essentially full
	memThrashCompressorMin = 0.30 //   or compressor >= 30% of RAM
)

// Verdict applies the thresholds above.
func (m MemorySnapshot) Verdict() MemoryVerdict {
	if m.PhysicalBytes <= 0 {
		return MemoryFine
	}
	if m.swapOfRAM() >= memThrashSwapOfRAM &&
		(m.SwapFill() >= memThrashSwapFill || m.CompressorShare() >= memThrashCompressorMin) {
		return MemoryThrashing
	}
	if m.swapOfRAM() >= memTightSwapOfRAM || m.CompressorShare() >= memTightCompressor {
		return MemoryTight
	}
	return MemoryFine
}

// VerdictLine is the one-line human verdict with the numbers that earned
// it, in the same honest style as the disk lines.
func (m MemorySnapshot) VerdictLine() string {
	switch m.Verdict() {
	case MemoryThrashing:
		return fmt.Sprintf("THRASHING — swap is %.0f%% full (%s, %.0fx of RAM) and the compressor holds %.0f%% of RAM: the machine is paging, not computing; quit the top compressed processes or add memory",
			100*m.SwapFill(), Bytes(m.SwapUsedBytes), m.swapOfRAM(), 100*m.CompressorShare())
	case MemoryTight:
		return fmt.Sprintf("tight — %s swapped (%.0f%% of RAM), compressor holds %.0f%% of RAM; one more big process tips this into paging",
			Bytes(m.SwapUsedBytes), 100*m.swapOfRAM(), 100*m.CompressorShare())
	default:
		return fmt.Sprintf("fine — %s swapped, compressor holds %.0f%% of RAM",
			Bytes(m.SwapUsedBytes), 100*m.CompressorShare())
	}
}

// memoryCmdTimeout bounds each shell-out; a thrashing machine is exactly
// when top is slow, and status must still return.
const memoryCmdTimeout = 10 * time.Second

// ReadMemory takes one live snapshot. Only the physical-RAM read is fatal;
// swap, vm_stat and top failures are tolerated and reported in Notes.
func ReadMemory(ctx context.Context) (MemorySnapshot, error) {
	var m MemorySnapshot
	run := func(name string, args ...string) (string, error) {
		cctx, cancel := context.WithTimeout(ctx, memoryCmdTimeout)
		defer cancel()
		out, err := exec.CommandContext(cctx, name, args...).Output()
		return string(out), err
	}

	out, err := run("/usr/sbin/sysctl", "-n", "hw.memsize", "vm.swapusage")
	if err != nil {
		return m, fmt.Errorf("sysctl hw.memsize vm.swapusage: %w", err)
	}
	phys, swapUsed, swapTotal, err := parseSysctlMemory(out)
	if err != nil {
		return m, err
	}
	m.PhysicalBytes, m.SwapUsedBytes, m.SwapTotalBytes = phys, swapUsed, swapTotal

	if out, err := run("/usr/bin/vm_stat"); err != nil {
		m.Notes = append(m.Notes, fmt.Sprintf("vm_stat unavailable: %v", err))
	} else if occ, stored, err := parseVMStatCompressor(out); err != nil {
		m.Notes = append(m.Notes, fmt.Sprintf("vm_stat unparseable: %v", err))
	} else {
		m.CompressorBytes, m.CompressedBytes = occ, stored
	}

	if out, err := run("/usr/bin/top", "-l", "1", "-o", "mem", "-stats", "pid,command,mem,cmprs", "-n", "3"); err != nil {
		m.Notes = append(m.Notes, fmt.Sprintf("top unavailable: %v", err))
	} else {
		m.TopCompressed = parseTopMemory(out, 3)
		if len(m.TopCompressed) == 0 {
			m.Notes = append(m.Notes, "top printed no process rows")
		}
	}
	return m, nil
}

// parseSysctlMemory reads the two-line `sysctl -n hw.memsize vm.swapusage`
// output: a byte count, then "total = 12288.00M  used = 11901.88M  free = ...".
func parseSysctlMemory(s string) (phys, swapUsed, swapTotal int64, err error) {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) < 1 {
		return 0, 0, 0, fmt.Errorf("sysctl: empty output")
	}
	phys, err = strconv.ParseInt(strings.TrimSpace(lines[0]), 10, 64)
	if err != nil || phys <= 0 {
		return 0, 0, 0, fmt.Errorf("sysctl hw.memsize: unparseable %q", lines[0])
	}
	if len(lines) < 2 {
		return phys, 0, 0, nil // no swapusage line: treat as no swap
	}
	swapTotal = swapField(lines[1], "total")
	swapUsed = swapField(lines[1], "used")
	return phys, swapUsed, swapTotal, nil
}

// swapField extracts "<key> = <value><unit>" from a vm.swapusage line.
func swapField(line, key string) int64 {
	f := strings.Fields(line)
	for i := 0; i+2 < len(f); i++ {
		if f[i] == key && f[i+1] == "=" {
			if b, ok := parseSizeSuffix(f[i+2]); ok {
				return b
			}
		}
	}
	return 0
}

// parseVMStatCompressor returns (occupied bytes, stored bytes) from vm_stat.
func parseVMStatCompressor(s string) (occupied, stored int64, err error) {
	pageSize := int64(4096)
	var occPages, storedPages int64
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Mach Virtual Memory Statistics"):
			// "(page size of 16384 bytes)"
			if i := strings.Index(line, "page size of "); i >= 0 {
				rest := strings.Fields(line[i+len("page size of "):])
				if len(rest) > 0 {
					if v, err := strconv.ParseInt(rest[0], 10, 64); err == nil && v > 0 {
						pageSize = v
					}
				}
			}
		case strings.HasPrefix(line, "Pages occupied by compressor:"):
			occPages = vmStatCount(line)
		case strings.HasPrefix(line, "Pages stored in compressor:"):
			storedPages = vmStatCount(line)
		}
	}
	if occPages == 0 && storedPages == 0 && !strings.Contains(s, "compressor") {
		return 0, 0, fmt.Errorf("no compressor lines")
	}
	return occPages * pageSize, storedPages * pageSize, nil
}

// vmStatCount parses the trailing "N." of a vm_stat row.
func vmStatCount(line string) int64 {
	i := strings.LastIndex(line, ":")
	if i < 0 {
		return 0
	}
	v, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(line[i+1:]), "."), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// parseTopMemory reads the process rows of
// `top -l 1 -o mem -stats pid,command,mem,cmprs -n N`: everything after the
// "PID COMMAND MEM CMPRS" header. Commands may contain spaces (top prints
// "Google Chrome He"), so pid is the first field, the two sizes the last
// two, and the command is whatever lies between. Rows are re-sorted by
// compressed bytes — top ordered them by resident memory — and capped at n.
func parseTopMemory(s string, n int) []MemoryProcess {
	var out []MemoryProcess
	inRows := false
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if f[0] == "PID" {
			inRows = true
			continue
		}
		if !inRows || len(f) < 4 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		mem, ok1 := parseSizeSuffix(f[len(f)-2])
		cmprs, ok2 := parseSizeSuffix(f[len(f)-1])
		if !ok1 || !ok2 {
			continue
		}
		out = append(out, MemoryProcess{
			PID:             pid,
			Command:         strings.Join(f[1:len(f)-2], " "),
			ResidentBytes:   mem,
			CompressedBytes: cmprs,
		})
	}
	// Insertion sort by CompressedBytes desc — n is tiny.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].CompressedBytes > out[j-1].CompressedBytes; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// parseSizeSuffix reads top/sysctl sizes: "9434M", "1763M+", "386.12M",
// "512K", "1.5G", "0B". A trailing +/- (top's delta marker) is ignored.
func parseSizeSuffix(s string) (int64, bool) {
	s = strings.TrimRight(strings.TrimSpace(s), "+-")
	if s == "" {
		return 0, false
	}
	mult := int64(1)
	switch s[len(s)-1] {
	case 'K':
		mult = 1 << 10
	case 'M':
		mult = 1 << 20
	case 'G':
		mult = 1 << 30
	case 'T':
		mult = 1 << 40
	case 'B':
		mult = 1
	default:
		s += "B" // bare number
	}
	v, err := strconv.ParseFloat(s[:len(s)-1], 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return int64(v * float64(mult)), true
}
