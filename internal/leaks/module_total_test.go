package leaks

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/modules"
)

// TestScanTotalIsUnionNotSumOfRows is the founding incident in miniature:
// two sibling hits that are APFS clones of each other. Each ROW honestly
// reports its own unique bytes (the full payload), but the module TOTAL
// must be the union — one payload, not two — because that is all a delete
// of both can free.
func TestScanTotalIsUnionNotSumOfRows(t *testing.T) {
	base := resolvedTempDir(t)
	a := filepath.Join(base, "leak.aaaaaa")
	b := filepath.Join(base, "leak.bbbbbb")
	for _, d := range []string{a, b} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const payload = 4 * 1024 * 1024
	buf := make([]byte, payload)
	for i := range buf {
		buf[i] = byte(i) // non-zero so APFS cannot compress it away
	}
	if err := os.WriteFile(filepath.Join(a, "payload"), buf, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := unix.Clonefile(filepath.Join(a, "payload"), filepath.Join(b, "payload"), 0); err != nil {
		t.Skipf("clonefile unsupported here (not APFS?): %v", err)
	}

	sig := Signature{
		ID:        "test-clone-leak",
		Globs:     []string{filepath.Join(base, "leak.??????")},
		Staleness: StaleWhenLsofEmpty,
		Risk:      modules.RiskLow,
	}
	m := New([]Signature{sig}, core.NewSafety(nil, nil))
	m.probe = fakeProbe(false, "nothing open")

	rep, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Items) != 2 {
		t.Fatalf("want 2 items, got %d", len(rep.Items))
	}
	var rows core.Bytes
	for _, it := range rep.Items {
		if it.Size < payload*95/100 {
			t.Errorf("row %s Size = %d, want ~%d (its own unique bytes)", it.Path, it.Size, payload)
		}
		rows += it.Size
	}
	lo, hi := core.Bytes(payload*95/100), core.Bytes(payload*13/10)
	if rep.Total < lo || rep.Total > hi {
		t.Errorf("Total = %d, want ~%d (union of clone siblings); rows sum to %d", rep.Total, payload, rows)
	}
	if rep.Total >= rows {
		t.Errorf("Total %d must be below the sum of rows %d for clone siblings", rep.Total, rows)
	}
}
