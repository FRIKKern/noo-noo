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

// insertSuggestion inserts sg, failing the test on error, and returns its id.
// Split out so the recurrence-read test stays flat (gocyclo).
func insertSuggestion(t *testing.T, s *Store, sg StoredSuggestion) int64 {
	t.Helper()
	id, err := s.InsertSuggestion(sg)
	if err != nil {
		t.Fatalf("InsertSuggestion: %v", err)
	}
	return id
}

// classifyLeaksHistory walks a ListSuggestionsSince("leaks", …) result: it
// asserts every row is a leaks row, notes whether the dismissed and open ids
// appeared, and checks each carries the right DismissedAt nullability.
func classifyLeaksHistory(t *testing.T, hist []StoredSuggestion, dismissedID, openID int64) (sawDismissed, sawOpen bool) {
	t.Helper()
	for _, sg := range hist {
		if sg.Module != "leaks" {
			t.Errorf("ListSuggestionsSince leaked a %q row", sg.Module)
		}
		switch sg.ID {
		case dismissedID:
			sawDismissed = true
			if sg.DismissedAt == nil {
				t.Errorf("dismissed row should carry a non-nil DismissedAt")
			}
		case openID:
			sawOpen = true
			if sg.DismissedAt != nil {
				t.Errorf("open row should carry a nil DismissedAt")
			}
		}
	}
	return sawDismissed, sawOpen
}

// TestListSuggestionsSinceIncludesDismissed is the recurrence-read contract:
// unlike ListOpenSuggestions, ListSuggestionsSince must return dismissed rows
// too, because a leak class re-appearing across dismissals IS the pattern
// `noo-noo trends` surfaces.
func TestListSuggestionsSinceIncludesDismissed(t *testing.T) {
	s := mustStore(t)
	defer func() { _ = s.Close() }()

	base := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	// A leak surfaced last week, then dismissed (cleaned).
	dismissedID := insertSuggestion(t, s, StoredSuggestion{
		Ts: base, Module: "leaks", Target: "/private/var/folders/x/code_sign_clone",
		Reason: "chrome clone re-leak", Severity: "high",
	})
	if err := s.DismissSuggestion(dismissedID, base.Add(time.Hour)); err != nil {
		t.Fatalf("DismissSuggestion: %v", err)
	}
	// The same class re-leaked today: a fresh open row.
	openID := insertSuggestion(t, s, StoredSuggestion{
		Ts: base.Add(7 * 24 * time.Hour), Module: "leaks", Target: "/private/var/folders/y/code_sign_clone",
		Reason: "chrome clone re-leak", Severity: "high",
	})
	// A different module's row in the window must not appear.
	insertSuggestion(t, s, StoredSuggestion{
		Ts: base.Add(7 * 24 * time.Hour), Module: "dev", Target: "/repo", Reason: "idle", Severity: "low",
	})

	// The open list omits the dismissed one.
	open, err := s.ListOpenSuggestions()
	if err != nil {
		t.Fatal(err)
	}
	for _, sg := range open {
		if sg.ID == dismissedID {
			t.Fatalf("ListOpenSuggestions must not return the dismissed row")
		}
	}

	// The recurrence read returns BOTH the dismissed and the open leaks row.
	hist, err := s.ListSuggestionsSince("leaks", base.Add(-time.Hour))
	if err != nil {
		t.Fatalf("ListSuggestionsSince: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("expected 2 leaks rows (dismissed + open), got %d: %+v", len(hist), hist)
	}
	sawDismissed, sawOpen := classifyLeaksHistory(t, hist, dismissedID, openID)
	if !sawDismissed || !sawOpen {
		t.Fatalf("expected both dismissed and open rows; sawDismissed=%v sawOpen=%v", sawDismissed, sawOpen)
	}

	// The `since` filter still bounds the window.
	recent, err := s.ListSuggestionsSince("leaks", base.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || recent[0].ID != openID {
		t.Fatalf("since-filter should leave only the recent open row, got %+v", recent)
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
