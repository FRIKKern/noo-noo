package metrics

import (
	"encoding/binary"
	"testing"
)

// xswUsageBytes builds a struct xsw_usage as the kernel hands it out.
func xswUsageBytes(total, avail, used uint64, pagesize uint32, encrypted bool) []byte {
	b := make([]byte, xswUsageSize)
	binary.LittleEndian.PutUint64(b[0:8], total)
	binary.LittleEndian.PutUint64(b[8:16], avail)
	binary.LittleEndian.PutUint64(b[16:24], used)
	binary.LittleEndian.PutUint32(b[24:28], pagesize)
	if encrypted {
		binary.LittleEndian.PutUint32(b[28:32], 1)
	}
	return b
}

// loadavgBytes builds a struct loadavg (3 × fixpt_t, pad, long fscale).
func loadavgBytes(l1, l5, l15 float64, fscale int64) []byte {
	b := make([]byte, loadavgSize)
	for i, v := range []float64{l1, l5, l15} {
		binary.LittleEndian.PutUint32(b[i*4:i*4+4], uint32(v*float64(fscale)))
	}
	binary.LittleEndian.PutUint64(b[16:24], uint64(fscale))
	return b
}

// TestSysctlRawStructs pins the shape unix.SysctlRaw actually returns for
// vm.swapusage and vm.loadavg: binary structs, not the `sysctl -n` text.
// Before this, SampleSysctl failed on every real machine.
func TestSysctlRawStructs(t *testing.T) {
	r := &fakeReader{vals: map[string][]byte{
		"hw.memsize":   u64bytes(32 << 30),
		"vm.swapusage": xswUsageBytes(12<<30, 1<<30, 11<<30, 16384, true),
		"vm.loadavg":   loadavgBytes(3.25, 2.5, 1.75, 2048),
	}}
	got, err := SampleSysctlWith(r)
	if err != nil {
		t.Fatalf("SampleSysctlWith(raw structs): %v", err)
	}
	if got.SwapTotalBytes != 12<<30 || got.SwapUsedBytes != 11<<30 {
		t.Errorf("swap = used %d / total %d, want 11 GiB / 12 GiB", got.SwapUsedBytes, got.SwapTotalBytes)
	}
	if got.Load1 != 3.25 || got.Load5 != 2.5 || got.Load15 != 1.75 {
		t.Errorf("loadavg = %v %v %v, want 3.25 2.5 1.75", got.Load1, got.Load5, got.Load15)
	}
}

func TestSysctlRawRejectsWrongShapes(t *testing.T) {
	if _, _, ok := parseSwapUsageRaw([]byte("total = 1.00M used = 0.50M")); ok {
		t.Error("text must not decode as xsw_usage")
	}
	if _, _, ok := parseSwapUsageRaw(xswUsageBytes(1<<20, 0, 2<<20, 4096, false)); ok {
		t.Error("used > total must be rejected")
	}
	if _, _, _, ok := parseLoadAvgRaw(loadavgBytes(1, 1, 1, 0)); ok {
		t.Error("fscale 0 must be rejected")
	}
	if _, _, _, ok := parseLoadAvgRaw([]byte("{ 1.0 1.0 1.0 }")); ok {
		t.Error("text must not decode as struct loadavg")
	}
}
