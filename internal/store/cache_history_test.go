package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCacheHistoryRoundTrip(t *testing.T) {
	s := mustStore(t)
	defer func() { _ = s.Close() }()

	now := time.Date(2026, 5, 3, 3, 0, 0, 0, time.UTC)
	if err := s.RecordCacheSize("~/Library/Caches/com.apple.Safari", 1_000_000, now); err != nil {
		t.Fatalf("RecordCacheSize: %v", err)
	}
	if err := s.RecordCacheSize("~/Library/Caches/com.apple.Safari", 1_500_000, now.Add(24*time.Hour)); err != nil {
		t.Fatalf("RecordCacheSize: %v", err)
	}
	series, err := s.CacheSizeSeries("~/Library/Caches/com.apple.Safari", now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("CacheSizeSeries: %v", err)
	}
	if len(series) != 2 {
		t.Fatalf("expected 2 samples, got %d", len(series))
	}
	if series[0].Bytes != 1_000_000 || series[1].Bytes != 1_500_000 {
		t.Errorf("wrong bytes order: %+v", series)
	}
	if !series[0].At.Before(series[1].At) {
		t.Errorf("samples should be chronological")
	}
}

func TestCacheHistoryFilter(t *testing.T) {
	s := mustStore(t)
	defer func() { _ = s.Close() }()
	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		_ = s.RecordCacheSize("p", int64(100*i), base.Add(time.Duration(i)*24*time.Hour))
	}
	rows, _ := s.CacheSizeSeries("p", base.Add(5*24*time.Hour))
	if len(rows) != 5 {
		t.Errorf("expected 5 rows since day 5, got %d", len(rows))
	}
}

func mustStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}
