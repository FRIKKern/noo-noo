package core

import (
	"strings"
	"testing"
)

const (
	gib = int64(1) << 30
	mib = int64(1) << 20
)

// mibF is a runtime float so fractional expectations are not constant
// conversions (which Go rejects when inexact).
var mibF = float64(mib)

// Captured on the founding machine mid-thrash (2026-10-02).
const testSysctlOut = "8589934592\ntotal = 12288.00M  used = 11901.88M  free = 386.12M  (encrypted)\n"

const testVMStatOut = `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                                4227.
Pages active:                             63975.
Pages stored in compressor:             1872681.
Pages occupied by compressor:            196231.
Decompressions:                      8695966292.
`

const testTopOut = `Processes: 712 total, 8 running, 704 sleeping, 3516 threads
PhysMem: 7459M used (2501M wired, 3068M compressor), 141M unused.

PID    COMMAND          MEM   CMPRS
1365   cmux             9434M 9032M
409    WindowServer     1757M 1375M
84923  Google Chrome He 1795M 1763M+
`

func TestParseSysctlMemory(t *testing.T) {
	phys, used, total, err := parseSysctlMemory(testSysctlOut)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if phys != 8*gib {
		t.Errorf("phys = %d, want 8 GiB", phys)
	}
	if total != 12288*mib || used != int64(11901.88*mibF) {
		t.Errorf("swap = %d/%d", used, total)
	}
}

func TestParseVMStatCompressor(t *testing.T) {
	occ, stored, err := parseVMStatCompressor(testVMStatOut)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if occ != 196231*16384 || stored != 1872681*16384 {
		t.Errorf("occupied=%d stored=%d", occ, stored)
	}
}

// Rows come back ordered by COMPRESSED bytes (top sorted them by resident),
// commands with spaces survive, and top's "+" delta marker is ignored.
func TestParseTopMemoryOrdersByCompressed(t *testing.T) {
	rows := parseTopMemory(testTopOut, 3)
	if len(rows) != 3 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Command != "cmux" || rows[0].PID != 1365 || rows[0].CompressedBytes != 9032*mib {
		t.Errorf("row0 = %+v", rows[0])
	}
	if rows[1].Command != "Google Chrome He" || rows[1].CompressedBytes != 1763*mib {
		t.Errorf("row1 = %+v", rows[1])
	}
	if rows[2].Command != "WindowServer" {
		t.Errorf("row2 = %+v", rows[2])
	}
	if got := parseTopMemory(testTopOut, 1); len(got) != 1 {
		t.Errorf("cap n=1 gave %d rows", len(got))
	}
}

func TestMemoryVerdicts(t *testing.T) {
	cases := []struct {
		name string
		m    MemorySnapshot
		want MemoryVerdict
		line string
	}{
		{"quiet", MemorySnapshot{PhysicalBytes: 8 * gib, SwapUsedBytes: 512 * mib, SwapTotalBytes: 1 * gib, CompressorBytes: 600 * mib}, MemoryFine, "fine"},
		{"swap a third of RAM", MemorySnapshot{PhysicalBytes: 8 * gib, SwapUsedBytes: 3 * gib, SwapTotalBytes: 4 * gib, CompressorBytes: 1 * gib}, MemoryTight, "tight"},
		{"big compressor, little swap", MemorySnapshot{PhysicalBytes: 8 * gib, SwapUsedBytes: 200 * mib, SwapTotalBytes: 1 * gib, CompressorBytes: 2200 * mib}, MemoryTight, "tight"},
		{"founding jetsam day", MemorySnapshot{PhysicalBytes: 8 * gib, SwapUsedBytes: 11901 * mib, SwapTotalBytes: 12288 * mib, CompressorBytes: 3068 * mib}, MemoryThrashing, "THRASHING"},
		{"deep swap, half-empty files, small compressor", MemorySnapshot{PhysicalBytes: 8 * gib, SwapUsedBytes: 5 * gib, SwapTotalBytes: 12 * gib, CompressorBytes: 1 * gib}, MemoryTight, "tight"},
		{"unknown RAM", MemorySnapshot{}, MemoryFine, "fine"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.m.Verdict(); got != c.want {
				t.Errorf("Verdict = %s, want %s", got, c.want)
			}
			if l := c.m.VerdictLine(); !strings.HasPrefix(l, c.line) {
				t.Errorf("VerdictLine = %q, want prefix %q", l, c.line)
			}
		})
	}
}

func TestParseSizeSuffix(t *testing.T) {
	for in, want := range map[string]int64{
		"9434M": 9434 * mib, "1763M+": 1763 * mib, "386.12M": int64(386.12 * mibF),
		"512K": 512 << 10, "1.5G": 3 << 29, "0B": 0, "42": 42,
	} {
		got, ok := parseSizeSuffix(in)
		if !ok || got != want {
			t.Errorf("parseSizeSuffix(%q) = %d,%v want %d", in, got, ok, want)
		}
	}
	if _, ok := parseSizeSuffix("junk"); ok {
		t.Error("junk parsed")
	}
}
