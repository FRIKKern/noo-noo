package pressure

import "testing"

const vmstatFixture = `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                              100000.
Pages active:                            300000.
Pages inactive:                          100000.
Pages speculative:                        20000.
Pages throttled:                              0.
Pages wired down:                        100000.
Pages purgeable:                          10000.
"Translation faults":                  12345678.
Pages copy-on-write:                     123456.
Pages zero filled:                     12345678.
Pages reactivated:                       234567.
Pages purged:                             34567.
File-backed pages:                       200000.
Anonymous pages:                         300000.
`

func TestParseVMStat(t *testing.T) {
	r, err := parseVMStat(vmstatFixture)
	if err != nil {
		t.Fatal(err)
	}
	// used = active + wired = 400000; total = 400000 + 100000 + 100000 = 600000
	// ratio = 400000/600000 = 0.6667
	want := 400000.0 / 600000.0
	if r.MemRatio < want-0.001 || r.MemRatio > want+0.001 {
		t.Errorf("MemRatio = %v, want ~%v", r.MemRatio, want)
	}
}

func TestParseVMStatBadInput(t *testing.T) {
	_, err := parseVMStat("not vm_stat output")
	if err == nil {
		t.Fatal("expected parse error")
	}
}

// Live test — only meaningful on macOS; safely returns nonzero ratio.
func TestVMStatSamplerLive(t *testing.T) {
	s := NewVMStatSampler()
	r, err := s.Sample()
	if err != nil {
		t.Skipf("vm_stat not available: %v", err)
	}
	if r.MemRatio <= 0 || r.MemRatio >= 1 {
		t.Errorf("live MemRatio = %v, want (0,1)", r.MemRatio)
	}
}
