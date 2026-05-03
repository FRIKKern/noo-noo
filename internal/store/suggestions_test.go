package store

import (
	"testing"
	"time"
)

func TestSuggestionsLifecycle(t *testing.T) {
	s := mustStore(t)
	defer func() { _ = s.Close() }()

	now := time.Date(2026, 5, 3, 3, 0, 0, 0, time.UTC)
	id, err := s.InsertSuggestion(StoredSuggestion{
		Ts: now, Module: "dev", Target: "/repo",
		Reason:   "idle 90d, node_modules 900MB",
		Evidence: map[string]string{"days": "90"},
		Severity: "low",
	})
	if err != nil {
		t.Fatalf("InsertSuggestion: %v", err)
	}
	open, err := s.ListOpenSuggestions()
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].ID != id {
		t.Fatalf("expected 1 open suggestion id %d, got %+v", id, open)
	}
	if err := s.DismissSuggestion(id, now.Add(time.Hour)); err != nil {
		t.Fatalf("Dismiss: %v", err)
	}
	open, _ = s.ListOpenSuggestions()
	if len(open) != 0 {
		t.Errorf("dismissed suggestion should not appear in open list, got %+v", open)
	}
}

func TestSuggestionsDedupe(t *testing.T) {
	s := mustStore(t)
	defer func() { _ = s.Close() }()
	now := time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC)
	first := StoredSuggestion{Ts: now, Module: "dev", Target: "/r", Reason: "x", Severity: "low"}
	if exists, _ := s.HasOpenSuggestion("dev", "/r"); exists {
		t.Fatal("should not exist yet")
	}
	if _, err := s.InsertSuggestion(first); err != nil {
		t.Fatal(err)
	}
	if exists, _ := s.HasOpenSuggestion("dev", "/r"); !exists {
		t.Error("expected open suggestion to exist")
	}
}
