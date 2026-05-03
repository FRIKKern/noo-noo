package metrics

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVMStatParseFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "vm_stat_fixture.txt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	got, err := ParseVMStat(data)
	if err != nil {
		t.Fatalf("ParseVMStat: %v", err)
	}
	if got.PageSize != 16384 {
		t.Errorf("PageSize = %d, want 16384", got.PageSize)
	}
	if got.PagesFree != 123456 {
		t.Errorf("PagesFree = %d, want 123456", got.PagesFree)
	}
	if got.PagesWired != 234567 {
		t.Errorf("PagesWired = %d, want 234567", got.PagesWired)
	}
	if got.SwapIns != 1234 || got.SwapOuts != 5678 {
		t.Errorf("swap: ins=%d outs=%d", got.SwapIns, got.SwapOuts)
	}
	// Derived: free bytes = PagesFree * PageSize.
	if got.FreeBytes() != 123456*16384 {
		t.Errorf("FreeBytes = %d", got.FreeBytes())
	}
}

func TestVMStatParseGarbage(t *testing.T) {
	_, err := ParseVMStat([]byte("not a vm_stat output"))
	if err == nil {
		t.Error("expected error on garbage input")
	}
}
