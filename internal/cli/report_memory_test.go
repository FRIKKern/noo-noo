package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/FRIKKern/noo-noo/internal/metrics"
)

const topFixture = `Processes: 700 total, 4 running, 696 sleeping, 3600 threads
2026/10/02 10:00:00
Load Avg: 3.1, 3.4, 3.6
PhysMem: 31G used (3000M wired, 9000M compressor), 500M unused.
Networks: packets: 1/1G in, 1/1G out.
Disks: 1/1T read, 1/1G written.

PID    COMMAND          MEM   CMPRS
1365   cmux             9494M 9132M
84923  node             1816M 1784M
409    WindowServer     1762M 1371M
61600  2.1.284          1139M 1081M
93935  Google Chrome He 727M  173M
777    small            10M   0B
778    swapped-out      100M  5000M
`

const psFixture = `  PID    RSS     ELAPSED COMM
    1  11024 04-18:47:36 /sbin/launchd
 1365 9721856 02-03:00:01 /Applications/cmux.app/Contents/MacOS/cmux
84923 1859584    01:22:33 /usr/local/bin/node
  409 1804288 04-18:45:11 /System/Library/PrivateFrameworks/SkyLight.framework/Resources/WindowServer
93935  744448       05:00 /Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Framework.framework/Versions/1/Helpers/Google Chrome Helper.app/Contents/MacOS/Google Chrome Helper
  778  102400 10-00:00:00 /usr/bin/swapped-out
`

// fakeMemRunner answers ps and top from fixtures, or fails on demand.
type fakeMemRunner struct {
	psErr, topErr error
}

func (f fakeMemRunner) Output(_ context.Context, _ []byte, name string, _ ...string) ([]byte, error) {
	switch {
	case strings.HasSuffix(name, "/ps"):
		if f.psErr != nil {
			return nil, f.psErr
		}
		return []byte(psFixture), nil
	case strings.HasSuffix(name, "/top"):
		if f.topErr != nil {
			return nil, f.topErr
		}
		return []byte(topFixture), nil
	}
	return nil, errors.New("unexpected command " + name)
}

func fakeMemSources(r fakeMemRunner) memSources {
	return memSources{
		sysctl: func() (metrics.SysInfo, error) {
			return metrics.SysInfo{MemSizeBytes: 32 << 30, SwapUsedBytes: 11 << 30, SwapTotalBytes: 12 << 30}, nil
		},
		vmstat: func() (metrics.VMStat, error) {
			// 4 GiB of pages held in 512 MiB of RAM.
			return metrics.VMStat{PageSize: 16384, PagesCompressed: 262144, PagesCompressorOccupied: 32768}, nil
		},
		run: r,
	}
}

func TestParseTopMemAndSizes(t *testing.T) {
	rows := parseTopMem([]byte(topFixture))
	if len(rows) != 7 {
		t.Fatalf("want 7 rows, got %d: %+v", len(rows), rows)
	}
	if rows[4].PID != 93935 || rows[4].Name != "Google Chrome He" || rows[4].ResidentBytes != 727<<20 || rows[4].CompressedBytes != 173<<20 {
		t.Errorf("multi-word command row wrong: %+v", rows[4])
	}
	for in, want := range map[string]int64{"9494M": 9494 << 20, "12G": 12 << 30, "0B": 0, "16K": 16 << 10, "100M+": 100 << 20, "42": 42} {
		if got, ok := parseTopSize(in); !ok || got != want {
			t.Errorf("parseTopSize(%q) = %d,%v want %d", in, got, ok, want)
		}
	}
	if _, ok := parseTopSize("junk"); ok {
		t.Error("junk should not parse")
	}
}

func TestParsePS(t *testing.T) {
	ps := parsePS([]byte(psFixture))
	r, ok := ps[93935]
	if !ok {
		t.Fatalf("pid 93935 missing: %v", ps)
	}
	if r.rssBytes != 744448*1024 || r.etime != "05:00" || !strings.HasSuffix(r.comm, "/Google Chrome Helper") {
		t.Errorf("row wrong: %+v", r)
	}
	if _, ok := ps[0]; ok {
		t.Error("header must not parse as a row")
	}
}

func TestGatherMemoryRanksByResidentPlusCompressed(t *testing.T) {
	m := gatherMemory(context.Background(), fakeMemSources(fakeMemRunner{}))
	if m.PhysicalBytes != 32<<30 || m.SwapUsedBytes != 11<<30 || m.SwapTotalBytes != 12<<30 ||
		m.CompressorBytes != 4<<30 || m.CompressorRAMBytes != 512<<20 {
		t.Errorf("headline numbers wrong: %+v", m)
	}
	if len(m.Top) != memTopN {
		t.Fatalf("want %d top rows, got %d", memTopN, len(m.Top))
	}
	// cmux (18.6G) > swapped-out (5.1G, resident alone would never place) > node > WindowServer > 2.1.284
	wantOrder := []int{1365, 778, 84923, 409, 61600}
	for i, pid := range wantOrder {
		if m.Top[i].PID != pid {
			t.Errorf("rank %d = pid %d, want %d (%+v)", i, m.Top[i].PID, pid, m.Top)
		}
	}
	if m.Top[0].Name != "cmux" || m.Top[0].Age != "02-03:00:01" {
		t.Errorf("name/age not joined from ps: %+v", m.Top[0])
	}
	if m.Top[4].Name != "2.1.284" || m.Top[4].Age != "" {
		t.Errorf("pid absent from ps should keep top's name and no age: %+v", m.Top[4])
	}
	if len(m.Notes) != 0 {
		t.Errorf("no notes expected, got %v", m.Notes)
	}
}

func TestGatherMemoryToleratesFailures(t *testing.T) {
	src := fakeMemSources(fakeMemRunner{topErr: errors.New("boom")})
	m := gatherMemory(context.Background(), src)
	if len(m.Top) != memTopN || m.Top[0].PID != 1365 || m.Top[0].CompressedBytes != 0 {
		t.Errorf("without top, rank on ps rss: %+v", m.Top)
	}
	if len(m.Notes) != 1 || !strings.Contains(m.Notes[0], "top failed") {
		t.Errorf("want one top-failed note, got %v", m.Notes)
	}

	src = fakeMemSources(fakeMemRunner{psErr: errors.New("no ps"), topErr: errors.New("no top")})
	src.sysctl = func() (metrics.SysInfo, error) { return metrics.SysInfo{}, errors.New("no sysctl") }
	src.vmstat = func() (metrics.VMStat, error) { return metrics.VMStat{}, errors.New("no vm_stat") }
	m = gatherMemory(context.Background(), src)
	var buf bytes.Buffer
	renderMemory(&buf, m)
	out := buf.String()
	for _, want := range []string{"Memory\n", "physical/swap unavailable", "top processes: unavailable"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
	if len(m.Notes) != 4 {
		t.Errorf("want 4 notes, got %v", m.Notes)
	}
}

func TestRenderMemoryHumanAndJSON(t *testing.T) {
	m := gatherMemory(context.Background(), fakeMemSources(fakeMemRunner{}))
	var buf bytes.Buffer
	renderMemory(&buf, m)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if lines[0] != "Memory" || lines[1] != "  32.0 GB physical, swap 11.0 GB / 12.0 GB used, compressor holding 4.0 GB in 512.0 MB of RAM" {
		t.Errorf("headline lines wrong:\n%s", buf.String())
	}
	if !strings.Contains(lines[3], "PID") || !strings.HasSuffix(lines[3], "NAME") {
		t.Errorf("table header wrong: %q", lines[3])
	}
	if !strings.Contains(lines[4], "1365") || !strings.Contains(lines[4], "9.3 GB") || !strings.HasSuffix(lines[4], "cmux") {
		t.Errorf("first row wrong: %q", lines[4])
	}

	buf.Reset()
	if err := renderMemoryJSON(&buf, m); err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal(buf.Bytes(), &row); err != nil {
		t.Fatalf("not one JSON object: %v\n%s", err, buf.String())
	}
	if row["module"] != "memory" || row["physical_bytes"] != float64(32<<30) || row["swap_total_bytes"] != float64(12<<30) {
		t.Errorf("json headline wrong: %v", row)
	}
	top, _ := row["top"].([]any)
	if len(top) != memTopN {
		t.Fatalf("json top wrong: %v", row["top"])
	}
	first, _ := top[0].(map[string]any)
	if first["pid"] != float64(1365) || first["name"] != "cmux" || first["age"] != "02-03:00:01" || first["compressed_bytes"] != float64(9132<<20) {
		t.Errorf("json first row wrong: %v", first)
	}
}
