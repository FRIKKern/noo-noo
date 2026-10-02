//go:build darwin

package metrics

import (
	"encoding/binary"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// SysInfo bundles physical memory, swap usage, and load average.
type SysInfo struct {
	MemSizeBytes   uint64
	SwapUsedBytes  int64
	SwapTotalBytes int64
	Load1          float64
	Load5          float64
	Load15         float64
}

type sysctlReader interface {
	Sysctl(name string) ([]byte, error)
}

type realReader struct{}

func (realReader) Sysctl(name string) ([]byte, error) {
	// unix.SysctlRaw returns the raw byte buffer for both ASCII and binary
	// MIBs. We use the raw form everywhere and let the parser decide.
	return unix.SysctlRaw(name)
}

// SampleSysctl reads memsize, swap, and loadavg from the real kernel.
func SampleSysctl() (SysInfo, error) {
	return SampleSysctlWith(realReader{})
}

// SampleSysctlWith reads via a custom reader (used by tests).
func SampleSysctlWith(r sysctlReader) (SysInfo, error) {
	var s SysInfo
	if b, err := r.Sysctl("hw.memsize"); err == nil && len(b) >= 8 {
		s.MemSizeBytes = binary.LittleEndian.Uint64(b[:8])
	} else if err != nil {
		return s, fmt.Errorf("hw.memsize: %w", err)
	}
	if b, err := r.Sysctl("vm.swapusage"); err == nil {
		// The raw MIB is struct xsw_usage, not the text `sysctl -n` prints;
		// the text form is still accepted for readers that hand it over.
		if used, total, ok := parseSwapUsageRaw(b); ok {
			s.SwapUsedBytes, s.SwapTotalBytes = used, total
		} else {
			used, perr := parseSwapUsage(string(b))
			if perr != nil {
				return s, perr
			}
			s.SwapUsedBytes = used
			// total is optional: an unparseable total leaves 0, never an error.
			s.SwapTotalBytes, _ = parseSwapField(string(b), swapTotalRE)
		}
	}
	if b, err := r.Sysctl("vm.loadavg"); err == nil {
		l1, l5, l15, ok := parseLoadAvgRaw(b)
		if !ok {
			var perr error
			if l1, l5, l15, perr = parseLoadAvg(string(b)); perr != nil {
				return s, perr
			}
		}
		s.Load1, s.Load5, s.Load15 = l1, l5, l15
	}
	return s, nil
}

// xswUsageSize is sizeof(struct xsw_usage): three u_int64 (total, avail,
// used), a u_int32 pagesize and a boolean_t encrypted — 32 bytes.
const xswUsageSize = 32

// parseSwapUsageRaw decodes the binary vm.swapusage MIB (struct xsw_usage,
// little-endian on every darwin target). ok=false when b is not that shape.
func parseSwapUsageRaw(b []byte) (used, total int64, ok bool) {
	if len(b) != xswUsageSize {
		return 0, 0, false
	}
	total = int64(binary.LittleEndian.Uint64(b[0:8]))
	used = int64(binary.LittleEndian.Uint64(b[16:24]))
	if total < 0 || used < 0 || used > total {
		return 0, 0, false
	}
	return used, total, true
}

// loadavgSize is sizeof(struct loadavg) on 64-bit darwin: fixpt_t ldavg[3]
// (3 × u_int32), 4 bytes of padding, then long fscale — 24 bytes.
const loadavgSize = 24

// parseLoadAvgRaw decodes the binary vm.loadavg MIB: each fixed-point
// average is ldavg[i] / fscale. ok=false when b is not that shape.
func parseLoadAvgRaw(b []byte) (l1, l5, l15 float64, ok bool) {
	if len(b) != loadavgSize {
		return 0, 0, 0, false
	}
	fscale := float64(int64(binary.LittleEndian.Uint64(b[16:24])))
	if fscale <= 0 {
		return 0, 0, 0, false
	}
	avg := func(off int) float64 { return float64(binary.LittleEndian.Uint32(b[off:off+4])) / fscale }
	return avg(0), avg(4), avg(8), true
}

var (
	swapUsedRE  = regexp.MustCompile(`used = ([0-9.]+)([KMG])`)
	swapTotalRE = regexp.MustCompile(`total = ([0-9.]+)([KMG])`)
)

// parseSwapUsage parses "total = ... used = NNN.NNM free = ..." into used bytes.
func parseSwapUsage(s string) (int64, error) { return parseSwapField(s, swapUsedRE) }

// parseSwapField extracts one "<name> = NNN.NNM" field of vm.swapusage as bytes.
func parseSwapField(s string, re *regexp.Regexp) (int64, error) {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("vm.swapusage: no match in %q", s)
	}
	val, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("parse swap value %q: %w", m[1], err)
	}
	mult := float64(1)
	switch m[2] {
	case "K":
		mult = 1024
	case "M":
		mult = 1024 * 1024
	case "G":
		mult = 1024 * 1024 * 1024
	}
	return int64(val * mult), nil
}

// parseLoadAvg parses "{ 1.20 0.85 0.43 }" into three floats.
func parseLoadAvg(s string) (l1, l5, l15 float64, err error) {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "{}")
	fields := strings.Fields(s)
	if len(fields) < 3 {
		return 0, 0, 0, fmt.Errorf("vm.loadavg: need 3 fields, got %q", s)
	}
	parse := func(idx int) (float64, error) { return strconv.ParseFloat(fields[idx], 64) }
	if l1, err = parse(0); err != nil {
		return
	}
	if l5, err = parse(1); err != nil {
		return
	}
	l15, err = parse(2)
	return
}
