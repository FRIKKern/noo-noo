package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/metrics"
)

// memTopN is how many processes the memory section names.
const memTopN = 5

// memTopFetch is how many rows to ask top for: it ranks by resident only,
// so re-ranking by resident+compressed needs a wider slate.
const memTopFetch = 15

// memProc is one process in the memory top list.
type memProc struct {
	PID             int    `json:"pid"`
	Name            string `json:"name"`
	Age             string `json:"age"` // ps etime: [[dd-]hh:]mm:ss
	ResidentBytes   int64  `json:"resident_bytes"`
	CompressedBytes int64  `json:"compressed_bytes"`
}

// memorySection is the report's memory posture. Zero fields with a note
// in Notes mean "unavailable", never "0".
type memorySection struct {
	PhysicalBytes  int64 `json:"physical_bytes"`
	SwapUsedBytes  int64 `json:"swap_used_bytes"`
	SwapTotalBytes int64 `json:"swap_total_bytes"`
	// CompressorBytes is the logical data held by the memory compressor
	// ("Pages stored in compressor"); CompressorRAMBytes is the physical RAM
	// it occupies for that ("Pages occupied by compressor").
	CompressorBytes    int64     `json:"compressor_bytes"`
	CompressorRAMBytes int64     `json:"compressor_ram_bytes"`
	Top                []memProc `json:"top"`
	Notes              []string  `json:"notes,omitempty"`
	haveSysctl         bool
	haveVMStat         bool
}

// memSources are the memory section's inputs: two in-process samplers and
// one runner for the two shell-outs (ps, top). Tests fake all three.
type memSources struct {
	sysctl func() (metrics.SysInfo, error)
	vmstat func() (metrics.VMStat, error)
	run    core.OutputRunner
}

func defaultMemSources() memSources {
	return memSources{sysctl: metrics.SampleSysctl, vmstat: metrics.SampleVMStat, run: core.ExecOutputRunner{}}
}

// gatherMemory assembles the section. Every source may fail independently;
// a failure becomes a note and the rest of the section still renders.
func gatherMemory(ctx context.Context, src memSources) memorySection {
	var m memorySection
	if si, err := src.sysctl(); err == nil {
		m.PhysicalBytes = int64(si.MemSizeBytes)
		m.SwapUsedBytes = si.SwapUsedBytes
		m.SwapTotalBytes = si.SwapTotalBytes
		m.haveSysctl = true
	} else {
		m.Notes = append(m.Notes, "physical/swap unavailable: "+err.Error())
	}
	if vs, err := src.vmstat(); err == nil {
		m.CompressorBytes = vs.PagesCompressed * vs.PageSize
		m.CompressorRAMBytes = vs.PagesCompressorOccupied * vs.PageSize
		m.haveVMStat = true
	} else {
		m.Notes = append(m.Notes, "compressor unavailable: "+err.Error())
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	psOut, psErr := src.run.Output(ctx, nil, "/bin/ps", "-Ao", "pid,rss,etime,comm")
	topOut, topErr := src.run.Output(ctx, nil, "/usr/bin/top", "-l", "1", "-o", "mem",
		"-stats", "pid,command,mem,cmprs", "-n", strconv.Itoa(memTopFetch))

	var ps map[int]psRow
	if psErr == nil {
		ps = parsePS(psOut)
	} else {
		m.Notes = append(m.Notes, "process ages unavailable: ps failed: "+psErr.Error())
	}
	var top []memProc
	switch {
	case topErr == nil:
		top = parseTopMem(topOut)
	case psErr == nil:
		// No per-process compressed figure without top; rank on resident alone.
		m.Notes = append(m.Notes, "per-process compressed memory unavailable: top failed: "+topErr.Error())
		for pid, r := range ps {
			top = append(top, memProc{PID: pid, Name: r.comm, ResidentBytes: r.rssBytes})
		}
	default:
		m.Notes = append(m.Notes, "top processes unavailable: top failed: "+topErr.Error())
	}
	for i := range top {
		if r, ok := ps[top[i].PID]; ok {
			top[i].Name = filepath.Base(r.comm) // top truncates names; ps has the whole path
			top[i].Age = r.etime
		}
	}
	sort.SliceStable(top, func(i, j int) bool {
		return top[i].ResidentBytes+top[i].CompressedBytes > top[j].ResidentBytes+top[j].CompressedBytes
	})
	if len(top) > memTopN {
		top = top[:memTopN]
	}
	m.Top = top
	return m
}

// psRow is one line of `ps -Ao pid,rss,etime,comm`.
type psRow struct {
	rssBytes int64
	etime    string
	comm     string
}

// parsePS parses `ps -Ao pid,rss,etime,comm` (rss is in KiB). comm may
// contain spaces ("Google Chrome Helper"), so it is the remainder of the
// line after the three fixed fields.
func parsePS(data []byte) map[int]psRow {
	out := map[int]psRow{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue // header
		}
		rssKB, _ := strconv.ParseInt(f[1], 10, 64)
		out[pid] = psRow{rssBytes: rssKB * 1024, etime: f[2], comm: strings.Join(f[3:], " ")}
	}
	return out
}

// parseTopMem parses the process table of `top -l 1 -stats pid,command,mem,cmprs`:
// the system summary is skipped up to the "PID  COMMAND ..." header; each row
// is PID, a command name that may contain spaces, then MEM and CMPRS at the
// end. Rows that do not parse are skipped.
func parseTopMem(data []byte) []memProc {
	var out []memProc
	sc := bufio.NewScanner(bytes.NewReader(data))
	inTable := false
	for sc.Scan() {
		line := sc.Text()
		f := strings.Fields(line)
		if !inTable {
			inTable = len(f) >= 2 && f[0] == "PID" && f[1] == "COMMAND"
			continue
		}
		if len(f) < 4 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		mem, ok1 := parseTopSize(f[len(f)-2])
		cmprs, ok2 := parseTopSize(f[len(f)-1])
		if !ok1 || !ok2 {
			continue
		}
		out = append(out, memProc{
			PID:             pid,
			Name:            strings.Join(f[1:len(f)-2], " "),
			ResidentBytes:   mem,
			CompressedBytes: cmprs,
		})
	}
	return out
}

// parseTopSize parses top's size cells: "9494M", "727M", "12G", "0B", "16K",
// optionally suffixed with a delta marker ("+"/"-").
func parseTopSize(s string) (int64, bool) {
	s = strings.TrimRight(s, "+-")
	if s == "" {
		return 0, false
	}
	unit := s[len(s)-1]
	num := s[:len(s)-1]
	mult := int64(1)
	switch unit {
	case 'B':
	case 'K':
		mult = 1 << 10
	case 'M':
		mult = 1 << 20
	case 'G':
		mult = 1 << 30
	case 'T':
		mult = 1 << 40
	default:
		num = s // bare number, bytes
	}
	v, err := strconv.ParseFloat(num, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return int64(v * float64(mult)), true
}

// renderMemory writes the human section.
func renderMemory(out io.Writer, m memorySection) {
	_, _ = fmt.Fprintln(out, "Memory")
	if m.haveSysctl {
		swap := "no swap"
		if m.SwapTotalBytes > 0 || m.SwapUsedBytes > 0 {
			swap = fmt.Sprintf("swap %s / %s used", core.Bytes(m.SwapUsedBytes), core.Bytes(m.SwapTotalBytes))
		}
		comp := "compressor unavailable"
		if m.haveVMStat {
			comp = fmt.Sprintf("compressor holding %s in %s of RAM",
				core.Bytes(m.CompressorBytes), core.Bytes(m.CompressorRAMBytes))
		}
		_, _ = fmt.Fprintf(out, "  %s physical, %s, %s\n", core.Bytes(m.PhysicalBytes), swap, comp)
	} else {
		_, _ = fmt.Fprintln(out, "  physical/swap unavailable")
	}
	if len(m.Top) == 0 {
		_, _ = fmt.Fprintln(out, "  top processes: unavailable")
	} else {
		_, _ = fmt.Fprintf(out, "  top %d by resident+compressed:\n", len(m.Top))
		_, _ = fmt.Fprintf(out, "  %6s  %*s  %*s  %-12s  %s\n", "PID", sizeColWidth, "RESIDENT", sizeColWidth, "COMPRESSED", "AGE", "NAME")
		for _, p := range m.Top {
			_, _ = fmt.Fprintf(out, "  %6d  %*s  %*s  %-12s  %s\n", p.PID,
				sizeColWidth, core.Bytes(p.ResidentBytes), sizeColWidth, core.Bytes(p.CompressedBytes), p.Age, p.Name)
		}
	}
	for _, n := range m.Notes {
		_, _ = fmt.Fprintf(out, "  note: %s\n", n)
	}
}

// renderMemoryJSON writes the section as one NDJSON row tagged module=memory.
func renderMemoryJSON(out io.Writer, m memorySection) error {
	row := struct {
		Module string `json:"module"`
		memorySection
	}{Module: "memory", memorySection: m}
	if row.Top == nil {
		row.Top = []memProc{}
	}
	return json.NewEncoder(out).Encode(row)
}
