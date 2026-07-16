package ipc

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/store"
)

// TestSuggestionSizeSurvivesStoreRoundTrip closes the proven string-type
// hole: stored evidence values are strings, so before parseSizeBytes every
// suggestion read back over IPC carried size=0. A suggestion inserted with
// the evidence_json carry (charter D16/D17) must read back with a non-zero
// first-class SizeBytes through Suggestions.List — the exact path the
// menubar and autoclean consume.
func TestSuggestionSizeSurvivesStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	if _, err := st.InsertSuggestion(store.StoredSuggestion{
		Ts:       time.Now(),
		Module:   "leaks",
		Target:   "/private/tmp/claude-x",
		Reason:   "chrome-code-sign-clone: 2.0 GB really reclaimable (proven stale)",
		Severity: "low",
		Evidence: map[string]string{
			"size_bytes": "2147483648",
			"signature":  "chrome-code-sign-clone",
		},
	}); err != nil {
		t.Fatalf("seed suggestion: %v", err)
	}

	svc := &SuggestionsService{Store: st}
	var resp SuggestionsResponse
	if err := svc.List(SuggestionsRequest{}, &resp); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("Items = %d, want 1", len(resp.Items))
	}
	if got, want := resp.Items[0].SizeBytes, int64(2147483648); got != want {
		t.Errorf("round-tripped SizeBytes = %d, want %d (string-type hole reopened)", got, want)
	}
}

func TestParseSizeBytes(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"123", 123},
		{"2147483648", 2147483648},
		{"1.5e+09", 1_500_000_000}, // defensive: JSON float rendering
		{"garbage", 0},
	}
	for _, c := range cases {
		if got := parseSizeBytes(c.in); got != c.want {
			t.Errorf("parseSizeBytes(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
