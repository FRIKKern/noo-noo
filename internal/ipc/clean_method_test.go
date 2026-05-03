package ipc

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/store"
)

// TestCleanExecuteRecordsAction asserts that Clean.Execute appends one audit
// row per target and returns a populated summary. The daemon does not perform
// the actual deletes in Phase 0.2 (the user runs `noo-noo dev clean` from
// Phase 0.1); Execute records that the user accepted a suggestion.
func TestCleanExecuteRecordsAction(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	fixed := time.Unix(1700000000, 0)
	svc := &CleanService{Store: st, Now: func() time.Time { return fixed }}
	var resp CleanResponse
	err = svc.Execute(CleanRequest{
		Targets: []CleanTarget{{Path: "/tmp/fake-cache", Bytes: 1024 * 1024 * 50}},
		DryRun:  true,
	}, &resp)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.BytesFreed != 50*1024*1024 {
		t.Errorf("BytesFreed = %d, want %d", resp.BytesFreed, 50*1024*1024)
	}
	if resp.Targets != 1 {
		t.Errorf("Targets = %d, want 1", resp.Targets)
	}
	// One audit row should be present.
	rows, err := st.ListActions(fixed.Add(-time.Hour), 10)
	if err != nil {
		t.Fatalf("ListActions: %v", err)
	}
	if len(rows) != 1 || rows[0].Op != "clean" || rows[0].Target != "/tmp/fake-cache" {
		t.Errorf("audit row wrong: %+v", rows)
	}
	if rows[0].Outcome != "dry-run" {
		t.Errorf("outcome = %q, want dry-run", rows[0].Outcome)
	}
}

// TestCleanExecuteEmpty asserts the no-target call returns a zero summary
// without touching the audit table.
func TestCleanExecuteEmpty(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "x.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	svc := &CleanService{Store: st, Now: time.Now}
	var resp CleanResponse
	if err := svc.Execute(CleanRequest{}, &resp); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.BytesFreed != 0 || resp.Targets != 0 {
		t.Errorf("expected zero result, got %+v", resp)
	}
}

// TestCleanExecuteRejectsEmptyPath asserts a target with no Path is a
// boundary error rather than silently inserting an audit row keyed on "".
func TestCleanExecuteRejectsEmptyPath(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "y.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	svc := &CleanService{Store: st, Now: time.Now}
	var resp CleanResponse
	if err := svc.Execute(CleanRequest{Targets: []CleanTarget{{Path: "", Bytes: 1}}}, &resp); err == nil {
		t.Error("expected error for empty target path, got nil")
	}
}
