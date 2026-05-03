package ipc

import (
	"fmt"
	"time"
)

// List returns every undismissed suggestion, newest first. The store rows are
// projected onto the wire type (heuristics.Suggestion) by suggestionFromStored
// so callers see a consistent shape across Report.Full and Suggestions.List.
func (s *SuggestionsService) List(_ SuggestionsRequest, resp *SuggestionsResponse) error {
	if s.Store == nil {
		return fmt.Errorf("suggestions: store not configured")
	}
	rows, err := s.Store.ListOpenSuggestions()
	if err != nil {
		return fmt.Errorf("list open suggestions: %w", err)
	}
	items := make([]SuggestionAlias, 0, len(rows))
	for _, r := range rows {
		items = append(items, suggestionFromStored(r))
	}
	resp.GeneratedAt = time.Now()
	resp.Items = items
	return nil
}

// Dismiss marks one suggestion as dismissed (sets dismissed_at = now). It
// rejects non-positive IDs and returns an error if no matching open row
// exists, so the CLI can surface "already dismissed / unknown id" instead of
// silently no-op'ing.
func (s *SuggestionsService) Dismiss(req DismissRequest, resp *DismissResponse) error {
	if s.Store == nil {
		return fmt.Errorf("suggestions: store not configured")
	}
	if req.ID <= 0 {
		return fmt.Errorf("dismiss: invalid id %d", req.ID)
	}

	// Pre-check: ListOpenSuggestions is the cheapest way to assert the row
	// exists and is still open without a new store API. The caller flow is
	// "list -> pick -> dismiss" so the working set is small.
	rows, err := s.Store.ListOpenSuggestions()
	if err != nil {
		return fmt.Errorf("dismiss: lookup: %w", err)
	}
	found := false
	for _, r := range rows {
		if r.ID == req.ID {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("dismiss: id %d not found among open suggestions", req.ID)
	}

	if err := s.Store.DismissSuggestion(req.ID, time.Now()); err != nil {
		return fmt.Errorf("dismiss: %w", err)
	}
	resp.OK = true
	return nil
}
