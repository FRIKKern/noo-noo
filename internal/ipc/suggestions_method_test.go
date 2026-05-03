package ipc

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/store"
)

// TestSuggestionsListAndDismiss covers the happy path: an inserted, open
// suggestion appears in List, and after Dismiss it no longer does.
func TestSuggestionsListAndDismiss(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	id, err := st.InsertSuggestion(store.StoredSuggestion{
		Ts:       time.Now(),
		Module:   "idle_repos",
		Target:   "/Users/me/old-thing",
		Reason:   "no commits in 45d",
		Severity: "low",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	svc := &SuggestionsService{Store: st}
	var listResp SuggestionsResponse
	if err := svc.List(SuggestionsRequest{}, &listResp); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listResp.Items) != 1 || listResp.Items[0].ID != id {
		t.Fatalf("List = %+v, want one item with id=%d", listResp.Items, id)
	}
	if listResp.GeneratedAt.IsZero() {
		t.Error("GeneratedAt should be set")
	}

	var dismissResp DismissResponse
	if err := svc.Dismiss(DismissRequest{ID: id}, &dismissResp); err != nil {
		t.Fatalf("Dismiss: %v", err)
	}
	if !dismissResp.OK {
		t.Error("Dismiss response OK = false, want true")
	}

	var after SuggestionsResponse
	if err := svc.List(SuggestionsRequest{}, &after); err != nil {
		t.Fatalf("List after dismiss: %v", err)
	}
	if len(after.Items) != 0 {
		t.Errorf("after dismiss List should be empty, got %d", len(after.Items))
	}
}

// TestSuggestionsDismissUnknownID asserts that dismissing an id with no
// matching open row returns an error rather than silently succeeding.
func TestSuggestionsDismissUnknownID(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "x.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	svc := &SuggestionsService{Store: st}
	if err := svc.Dismiss(DismissRequest{ID: 99999}, &DismissResponse{}); err == nil {
		t.Error("expected error for unknown id, got nil")
	}
}

// TestSuggestionsDismissInvalidID rejects non-positive IDs at the boundary
// before touching the store.
func TestSuggestionsDismissInvalidID(t *testing.T) {
	svc := &SuggestionsService{}
	if err := svc.Dismiss(DismissRequest{ID: 0}, &DismissResponse{}); err == nil {
		t.Error("expected error for id=0, got nil")
	}
}
