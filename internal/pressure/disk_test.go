package pressure

import "testing"

func TestDiskSamplerRoot(t *testing.T) {
	s := NewDiskSampler("/")
	r, err := s.Sample()
	if err != nil {
		t.Fatal(err)
	}
	if r.FreeDiskGB <= 0 {
		t.Fatalf("free disk = %v, expected positive", r.FreeDiskGB)
	}
	// Reasonable upper sanity bound (no Mac has 100 PB free).
	if r.FreeDiskGB > 1e8 {
		t.Errorf("suspiciously large free disk: %v GB", r.FreeDiskGB)
	}
}

func TestDiskSamplerBadPath(t *testing.T) {
	s := NewDiskSampler("/this/does/not/exist")
	_, err := s.Sample()
	if err == nil {
		t.Fatal("expected error for nonexistent path")
	}
}
