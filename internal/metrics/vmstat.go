// Package metrics reads cheap, system-wide signals (memory pressure, load
// average, swap activity) for use by heuristics. Pure Go where possible;
// shell-outs only when stdlib doesn't expose the data.
package metrics

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// VMStat is the parsed output of macOS "vm_stat".
type VMStat struct {
	PageSize        int64 // bytes
	PagesFree       int64
	PagesActive     int64
	PagesWired      int64
	PagesCompressed int64 // "Pages stored in compressor"
	SwapIns         int64
	SwapOuts        int64
}

// FreeBytes is PagesFree * PageSize.
func (v VMStat) FreeBytes() int64 { return v.PagesFree * v.PageSize }

// SampleVMStat shells out to /usr/bin/vm_stat and parses the result.
func SampleVMStat() (VMStat, error) {
	out, err := exec.Command("/usr/bin/vm_stat").Output()
	if err != nil {
		return VMStat{}, fmt.Errorf("run vm_stat: %w", err)
	}
	return ParseVMStat(out)
}

var headerRE = regexp.MustCompile(`page size of (\d+) bytes`)

// ParseVMStat parses raw vm_stat output. Exported for testability.
func ParseVMStat(data []byte) (VMStat, error) {
	var v VMStat
	scanner := bufio.NewScanner(bytes.NewReader(data))
	if !scanner.Scan() {
		return v, fmt.Errorf("empty vm_stat output")
	}
	header := scanner.Text()
	m := headerRE.FindStringSubmatch(header)
	if m == nil {
		return v, fmt.Errorf("unexpected vm_stat header: %q", header)
	}
	ps, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return v, fmt.Errorf("parse page size: %w", err)
	}
	v.PageSize = ps

	for scanner.Scan() {
		line := scanner.Text()
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line[idx+1:]), "."))
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			continue // skip non-numeric lines
		}
		switch key {
		case "Pages free":
			v.PagesFree = n
		case "Pages active":
			v.PagesActive = n
		case "Pages wired down":
			v.PagesWired = n
		case "Pages stored in compressor":
			v.PagesCompressed = n
		case "Swapins":
			v.SwapIns = n
		case "Swapouts":
			v.SwapOuts = n
		}
	}
	if err := scanner.Err(); err != nil {
		return v, fmt.Errorf("scan vm_stat: %w", err)
	}
	return v, nil
}
