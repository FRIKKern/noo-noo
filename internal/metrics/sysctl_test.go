//go:build darwin

package metrics

import (
	"encoding/binary"
	"testing"
)

type fakeReader struct {
	vals map[string][]byte
	errs map[string]error
}

func (f *fakeReader) Sysctl(name string) ([]byte, error) {
	if err, ok := f.errs[name]; ok {
		return nil, err
	}
	return f.vals[name], nil
}

func u64bytes(v uint64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, v)
	return b
}

func TestSysctlMemSize(t *testing.T) {
	r := &fakeReader{vals: map[string][]byte{
		"hw.memsize":   u64bytes(34_359_738_368), // 32 GiB
		"vm.swapusage": []byte("total = 2048.00M  used = 512.50M  free = 1535.50M  (encrypted)"),
		"vm.loadavg":   []byte("{ 1.20 1.05 0.95 }"),
	}}
	got, err := SampleSysctlWith(r)
	if err != nil {
		t.Fatalf("SampleSysctl: %v", err)
	}
	if got.MemSizeBytes != 34_359_738_368 {
		t.Errorf("MemSizeBytes = %d", got.MemSizeBytes)
	}
	if got.SwapUsedBytes < 500_000_000 || got.SwapUsedBytes > 600_000_000 {
		t.Errorf("SwapUsedBytes out of range: %d", got.SwapUsedBytes)
	}
	if got.Load1 != 1.20 || got.Load5 != 1.05 || got.Load15 != 0.95 {
		t.Errorf("loadavg parse wrong: %+v", got)
	}
}

func TestParseSwapUsage(t *testing.T) {
	got, err := parseSwapUsage("total = 1024.00M  used = 256.00M  free = 768.00M")
	if err != nil {
		t.Fatalf("parseSwapUsage: %v", err)
	}
	want := int64(256 * 1024 * 1024)
	if got < want-1024 || got > want+1024 {
		t.Errorf("got %d, want ~%d", got, want)
	}
}

func TestParseLoadAvg(t *testing.T) {
	l1, l5, l15, err := parseLoadAvg("{ 0.50 0.30 0.10 }")
	if err != nil {
		t.Fatalf("parseLoadAvg: %v", err)
	}
	if l1 != 0.50 || l5 != 0.30 || l15 != 0.10 {
		t.Errorf("parseLoadAvg = %v %v %v", l1, l5, l15)
	}
}
