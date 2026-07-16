package autoclean

import (
	"testing"

	"github.com/FRIKKern/noo-noo/internal/heuristics"
)

// TestSuggestionSizePrefersSizeBytes: the first-class field (charter D17)
// wins over whatever the evidence map says; the evidence fallback keeps
// working — now including the string form the store round-trip produces.
func TestSuggestionSizePrefersSizeBytes(t *testing.T) {
	cases := []struct {
		name string
		s    heuristics.Suggestion
		want int64
	}{
		{
			name: "first-class field wins over conflicting evidence",
			s: heuristics.Suggestion{
				SizeBytes: 5 << 30,
				Evidence:  map[string]any{"size_bytes": int64(1)},
			},
			want: 5 << 30,
		},
		{
			name: "string evidence parses (store round-trip form)",
			s:    heuristics.Suggestion{Evidence: map[string]any{"size_bytes": "2147483648"}},
			want: 2147483648,
		},
		{
			name: "float-string evidence parses defensively",
			s:    heuristics.Suggestion{Evidence: map[string]any{"size_bytes": "1.5e+09"}},
			want: 1_500_000_000,
		},
		{
			name: "unparseable string is 0 (fails the size gate)",
			s:    heuristics.Suggestion{Evidence: map[string]any{"size_bytes": "garbage"}},
			want: 0,
		},
		{
			name: "int64 evidence still works",
			s:    heuristics.Suggestion{Evidence: map[string]any{"size_bytes": int64(42)}},
			want: 42,
		},
		{
			name: "float64 evidence still works",
			s:    heuristics.Suggestion{Evidence: map[string]any{"size_bytes": float64(43)}},
			want: 43,
		},
		{
			name: "nothing set is 0",
			s:    heuristics.Suggestion{},
			want: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := suggestionSize(c.s); got != c.want {
				t.Errorf("suggestionSize = %d, want %d", got, c.want)
			}
		})
	}
}

// TestLeaksModuleNeverAutoCleaned pins the law that the leaks module stays
// OUTSIDE ModulesAllowed: even a fully-enabled, risk-acknowledged config
// with a huge idle suggestion refuses a leaks-module row at the allowlist
// gate. The daemon SURFACES leaks; it never deletes them unattended.
func TestLeaksModuleNeverAutoCleaned(t *testing.T) {
	cfg := Config{
		Enabled:            true,
		RiskAcknowledgedAt: "2026-07-15T00:00:00Z",
		ModulesAllowed:     []string{"dev"}, // the shipped default
		MinIdleDays:        0,
		MinSizeMB:          0,
	}
	s := heuristics.Suggestion{
		Module:    "leaks",
		Target:    "/private/tmp/claude-x",
		SizeBytes: 50 << 30,
		Evidence:  map[string]any{"idle_days": 400},
	}
	action, ok := EvaluateSuggestion(s, cfg)
	if ok {
		t.Fatal("leaks suggestion passed the gates — the allowlist law is broken")
	}
	if action.SkipReason != "module_not_allowed" {
		t.Errorf("SkipReason = %q, want module_not_allowed", action.SkipReason)
	}
}
