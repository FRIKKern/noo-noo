package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/store"
)

// newFixtureStore builds a real SQLite store in a temp dir with:
//   - a cache target growing 100 MB/day for 5 days, plus a same-day burst of
//     5-minute samples (the mixed cadence day-bucketing must collapse);
//   - a single-sample cache target (insufficient history);
//   - internal-disk samples filling 1 GB/day for 4 days;
//   - a leak suggestion (dismissed!) recurring on two distinct days, and a
//     one-off suggestion that must NOT become a pattern.
func newFixtureStore(t *testing.T, now time.Time) (string, *store.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open fixture store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	day := func(daysAgo int) time.Time { return now.Add(-time.Duration(daysAgo) * 24 * time.Hour) }

	// Growing cache: 100 MB/day over days 4..0.
	for i := 4; i >= 0; i-- {
		if err := st.RecordCacheSize("/caches/pnpm", int64((5-i))*100_000_000, day(i)); err != nil {
			t.Fatal(err)
		}
	}
	// Same-day 5-minute burst on the newest day: 20 samples that must
	// collapse into the single day bucket (last one wins).
	for j := 0; j < 20; j++ {
		if err := st.RecordCacheSize("/caches/pnpm", 500_000_000+int64(j),
			day(0).Add(time.Duration(j)*5*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	// Single-sample target: honest insufficient history.
	if err := st.RecordCacheSize("/caches/lonely", 42_000_000, day(0)); err != nil {
		t.Fatal(err)
	}

	// Internal disk: 250 GB total, filling 1 GB/day, 4 days of history.
	for i := 3; i >= 0; i-- {
		used := int64(200+(3-i)) * 1_000_000_000
		if err := st.RecordDiskSpace("BOOT-UUID", "/", 250_000_000_000,
			250_000_000_000-used, day(i)); err != nil {
			t.Fatal(err)
		}
	}

	// Recurring leak: same signature on two distinct days; the first
	// occurrence was DISMISSED (cleaned) — recurrence must still see it.
	id, err := st.InsertSuggestion(store.StoredSuggestion{
		Ts: day(7), Module: "leaks", Target: "/private/var/folders/x/a.code_sign_clone",
		Reason: "stale clone", Severity: "medium",
		Evidence: map[string]string{
			"signature":  "chrome-code-sign-clone",
			"workaround": "chrome --disable-features=MacAppCodeSignClone",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DismissSuggestion(id, day(6)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertSuggestion(store.StoredSuggestion{
		Ts: day(0), Module: "leaks", Target: "/private/var/folders/x/b.code_sign_clone",
		Reason: "stale clone", Severity: "medium",
		Evidence: map[string]string{
			"signature":  "chrome-code-sign-clone",
			"workaround": "chrome --disable-features=MacAppCodeSignClone",
		},
	}); err != nil {
		t.Fatal(err)
	}
	// One-off: seen once, never a pattern.
	if _, err := st.InsertSuggestion(store.StoredSuggestion{
		Ts: day(2), Module: "leaks", Target: "/Library/Caches/x.ShipIt",
		Reason: "stale updater", Severity: "low",
		Evidence: map[string]string{"signature": "shipit-updater"},
	}); err != nil {
		t.Fatal(err)
	}
	return path, st
}

func runTrends(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var out, errBuf bytes.Buffer
	app := &App{Out: &out, Err: &errBuf}
	code := app.Run(context.Background(), append([]string{"noo-noo", "trends"}, args...))
	return out.String(), errBuf.String(), code
}

func TestTrendsHuman(t *testing.T) {
	now := time.Now().UTC().Truncate(24 * time.Hour).Add(10 * time.Hour)
	path, _ := newFixtureStore(t, now)

	out, errOut, code := runTrends(t, "-store", path)
	if code != 0 {
		t.Fatalf("exit = %d (stderr: %s)", code, errOut)
	}

	for _, want := range []string{
		"/caches/pnpm",
		"/day over", // a real fitted rate with a cited window
		"insufficient history (1 day) — check back tomorrow", // the lonely target
		"chrome-code-sign-clone — seen on 2 distinct days",
		"recurring — fix it permanently: chrome --disable-features=MacAppCodeSignClone",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("trends output missing %q\n---\n%s", want, out)
		}
	}
	// The one-off signature must NOT be rendered as a pattern.
	if strings.Contains(out, "shipit-updater") {
		t.Errorf("one-off signature rendered as a pattern:\n%s", out)
	}
	// Sparkline runes present.
	if !strings.ContainsAny(out, "▁▂▃▄▅▆▇█") {
		t.Errorf("no sparklines in output:\n%s", out)
	}
}

func TestTrendsJSON(t *testing.T) {
	now := time.Now().UTC().Truncate(24 * time.Hour).Add(10 * time.Hour)
	path, _ := newFixtureStore(t, now)

	out, errOut, code := runTrends(t, "-store", path, "-json")
	if code != 0 {
		t.Fatalf("exit = %d (stderr: %s)", code, errOut)
	}

	var caches []cacheTrendJSON
	var disks []diskTrendJSON
	var patterns []patternJSON
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var kind struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal([]byte(line), &kind); err != nil {
			t.Fatalf("bad NDJSON line %q: %v", line, err)
		}
		switch kind.Kind {
		case "cache_trend":
			var c cacheTrendJSON
			_ = json.Unmarshal([]byte(line), &c)
			caches = append(caches, c)
		case "disk_trend":
			var d diskTrendJSON
			_ = json.Unmarshal([]byte(line), &d)
			disks = append(disks, d)
		case "recurring_pattern":
			var p patternJSON
			_ = json.Unmarshal([]byte(line), &p)
			patterns = append(patterns, p)
		default:
			t.Fatalf("unknown kind %q in line %q", kind.Kind, line)
		}
	}

	// pnpm: 5 day-buckets (the 20-sample burst collapsed into day 0), and a
	// day-bucketed fit close to 100 MB/day — the burst must not distort it
	// beyond the bucket substitution (last burst sample 500 MB ≈ day-5 value).
	var pnpm *cacheTrendJSON
	for i := range caches {
		if caches[i].Target == "/caches/pnpm" {
			pnpm = &caches[i]
		}
	}
	if pnpm == nil {
		t.Fatalf("no pnpm cache_trend row: %+v", caches)
	}
	if pnpm.Points != 5 {
		t.Errorf("pnpm points = %d, want 5 day-buckets (burst must collapse)", pnpm.Points)
	}
	if !pnpm.FitOK || pnpm.BytesPerDay < 80_000_000 || pnpm.BytesPerDay > 120_000_000 {
		t.Errorf("pnpm fit = %+v, want ~100 MB/day", pnpm)
	}

	// lonely: fit refused, no fake precision.
	var lonely *cacheTrendJSON
	for i := range caches {
		if caches[i].Target == "/caches/lonely" {
			lonely = &caches[i]
		}
	}
	if lonely == nil || lonely.FitOK {
		t.Errorf("lonely target must have fit_ok=false: %+v", lonely)
	}

	// disk: filling 1 GB/day with ~46 GB free → ~46 days until full.
	if len(disks) != 1 {
		t.Fatalf("disk rows = %d, want 1", len(disks))
	}
	d := disks[0]
	if !d.FitOK || d.BytesPerDay < 900_000_000 || d.BytesPerDay > 1_100_000_000 {
		t.Errorf("disk fit = %+v, want ~1 GB/day", d)
	}
	if !d.FullOK || d.DaysUntilFull < 40 || d.DaysUntilFull > 52 {
		t.Errorf("days_until_full = %v (ok=%v), want ~46", d.DaysUntilFull, d.FullOK)
	}

	// recurrence: exactly one pattern, workaround carried verbatim, and the
	// dismissed first occurrence counted.
	if len(patterns) != 1 {
		t.Fatalf("patterns = %+v, want exactly 1", patterns)
	}
	p := patterns[0]
	if p.Signature != "chrome-code-sign-clone" || p.DistinctDays != 2 || p.Occurrences != 2 {
		t.Errorf("pattern = %+v", p)
	}
	if p.Workaround != "chrome --disable-features=MacAppCodeSignClone" {
		t.Errorf("workaround not verbatim: %q", p.Workaround)
	}
}

func TestTrendsEmptyStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	out, _, code := runTrends(t, "-store", path)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	for _, want := range []string{
		"No cache history yet",
		"No disk-space history yet",
		"No recurring patterns",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("empty-store output missing %q\n---\n%s", want, out)
		}
	}
}

func TestTrendsMissingStoreErrors(t *testing.T) {
	_, errOut, code := runTrends(t, "-store", filepath.Join(t.TempDir(), "nope.db"))
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut, "no daemon store at") {
		t.Errorf("missing honest store error: %q", errOut)
	}
}

// TestTrendsVerbSafety: positionals and unknown flags hard-error (charter
// D22 — never a silent no-op).
func TestTrendsVerbSafety(t *testing.T) {
	cases := [][]string{
		{"show"},
		{"show", "-json"},
		{"-days", "0"},
		{"-bogus"},
	}
	for _, args := range cases {
		_, errOut, code := runTrends(t, args...)
		if code != 2 {
			t.Errorf("trends %v: exit = %d, want 2 (stderr: %s)", args, code, errOut)
		}
	}
}
