package metrics

import "testing"

// TestParseVMStatCompressorOccupied pins the two compressor figures apart:
// "stored" is logical data held, "occupied" is the RAM it costs — on an
// 8 GB machine the first can read 26 GB while the second is 3 GB.
func TestParseVMStatCompressorOccupied(t *testing.T) {
	v, err := ParseVMStat([]byte(`Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                                4255.
Pages stored in compressor:             1728447.
Pages occupied by compressor:            203462.
`))
	if err != nil {
		t.Fatal(err)
	}
	if v.PagesCompressed != 1728447 || v.PagesCompressorOccupied != 203462 {
		t.Errorf("compressor pages = stored %d / occupied %d, want 1728447 / 203462", v.PagesCompressed, v.PagesCompressorOccupied)
	}
}
