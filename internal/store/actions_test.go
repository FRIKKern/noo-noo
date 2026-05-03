package store

import (
	"testing"
	"time"
)

func TestActionsRecordAndList(t *testing.T) {
	s := mustStore(t)
	defer func() { _ = s.Close() }()

	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	if _, err := s.RecordAction(StoredAction{
		Ts: now, Module: "dev", Op: "delete",
		Target: "/repo/node_modules", SizeBytes: 200_000_000,
		Evidence: map[string]string{"last_commit_days": "90"},
		Outcome:  "ok",
	}); err != nil {
		t.Fatalf("RecordAction: %v", err)
	}
	rows, err := s.ListActions(now.Add(-time.Hour), 10)
	if err != nil {
		t.Fatalf("ListActions: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Evidence["last_commit_days"] != "90" {
		t.Errorf("evidence not round-tripped: %+v", rows[0].Evidence)
	}
}
