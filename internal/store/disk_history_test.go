package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestDiskHistoryRoundTrip(t *testing.T) {
	s := mustStore(t)
	defer func() { _ = s.Close() }()

	const uuid = "0DBD1B63-0377-450B-A340-7E72D0925EBC"
	now := time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC)
	if err := s.RecordDiskSpace(uuid, "/Volumes/SATECHI", 2_000_000_000, 900_000_000, now); err != nil {
		t.Fatalf("RecordDiskSpace: %v", err)
	}
	if err := s.RecordDiskSpace(uuid, "/Volumes/SATECHI", 2_000_000_000, 700_000_000, now.Add(24*time.Hour)); err != nil {
		t.Fatalf("RecordDiskSpace: %v", err)
	}

	series, err := s.DiskSpaceSeries(uuid, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("DiskSpaceSeries: %v", err)
	}
	if len(series) != 2 {
		t.Fatalf("expected 2 samples, got %d", len(series))
	}
	if series[0].FreeBytes != 900_000_000 || series[1].FreeBytes != 700_000_000 {
		t.Errorf("wrong free-bytes order (fill should shrink free): %+v", series)
	}
	if series[0].TotalBytes != 2_000_000_000 {
		t.Errorf("total bytes = %d, want 2_000_000_000", series[0].TotalBytes)
	}
	if series[0].MountPoint != "/Volumes/SATECHI" {
		t.Errorf("mount point = %q", series[0].MountPoint)
	}
	if !series[0].At.Before(series[1].At) {
		t.Errorf("samples should be chronological")
	}
}

func TestDiskHistoryFilterAndKeying(t *testing.T) {
	s := mustStore(t)
	defer func() { _ = s.Close() }()
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		_ = s.RecordDiskSpace("A", "/", 100, int64(i), base.Add(time.Duration(i)*24*time.Hour))
	}
	// A different volume's rows must not bleed into A's series.
	_ = s.RecordDiskSpace("B", "/Volumes/Other", 500, 500, base.Add(3*24*time.Hour))

	rows, _ := s.DiskSpaceSeries("A", base.Add(5*24*time.Hour))
	if len(rows) != 5 {
		t.Errorf("expected 5 rows of A since day 5, got %d", len(rows))
	}
	for _, r := range rows {
		if r.VolumeUUID != "A" {
			t.Errorf("series leaked a %q row into A's query", r.VolumeUUID)
		}
	}
}

// TestDiskHistorySurvivesReopen proves the new table is created idempotently:
// a second Open of the same file (which re-execs schema.sql) must not error,
// and rows written before the re-open remain readable after it. This is the
// charter D16 guarantee for CREATE TABLE IF NOT EXISTS.
func TestDiskHistorySurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reopen.db")
	now := time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC)

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := s1.RecordDiskSpace("A", "/", 100, 40, now); err != nil {
		t.Fatalf("RecordDiskSpace: %v", err)
	}
	_ = s1.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open (schema re-exec must be idempotent): %v", err)
	}
	defer func() { _ = s2.Close() }()
	rows, err := s2.DiskSpaceSeries("A", now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("DiskSpaceSeries after reopen: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("row written before reopen should survive, got %d", len(rows))
	}
}
