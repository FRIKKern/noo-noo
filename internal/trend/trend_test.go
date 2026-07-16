package trend

import (
	"math"
	"testing"
	"time"
)

func day(d int) time.Time {
	return time.Date(2026, 7, 1+d, 0, 0, 0, 0, time.UTC)
}

// TestDayBucketCollapsesMixedCadence is the load-bearing proof for the whole
// package: the daemon's real series mix a ~5-minute pressure cadence with
// the daily tick, and the bucketer must collapse a burst of same-day samples
// into exactly one end-of-day point.
func TestDayBucketCollapsesMixedCadence(t *testing.T) {
	var samples []Sample
	// Day 0: 100 pressure-cadence samples 5 minutes apart, growing 1 MB
	// per sample. Only the last one (99 MB offset) may survive.
	base := day(0).Add(8 * time.Hour)
	for i := 0; i < 100; i++ {
		samples = append(samples, Sample{
			At:    base.Add(time.Duration(i) * 5 * time.Minute),
			Bytes: 1_000_000 * int64(i),
		})
	}
	// Days 1 and 2: a single daily-tick sample each.
	samples = append(samples,
		Sample{At: day(1).Add(3 * time.Hour), Bytes: 200_000_000},
		Sample{At: day(2).Add(3 * time.Hour), Bytes: 300_000_000},
	)

	pts := DayBucket(samples)
	if len(pts) != 3 {
		t.Fatalf("DayBucket buckets = %d, want 3 (one per distinct day)", len(pts))
	}
	if pts[0].Bytes != 99_000_000 {
		t.Errorf("day-0 bucket = %d, want last-of-day 99000000", pts[0].Bytes)
	}
	if !pts[0].Day.Equal(day(0)) || !pts[2].Day.Equal(day(2)) {
		t.Errorf("bucket days wrong: %v .. %v", pts[0].Day, pts[2].Day)
	}
}

func TestDayBucketUnsortedInput(t *testing.T) {
	// Same day, out of order: the chronologically-last sample must win.
	pts := DayBucket([]Sample{
		{At: day(0).Add(20 * time.Hour), Bytes: 500},
		{At: day(0).Add(2 * time.Hour), Bytes: 100},
	})
	if len(pts) != 1 || pts[0].Bytes != 500 {
		t.Fatalf("got %+v, want single bucket of 500", pts)
	}
}

func TestDayBucketEmpty(t *testing.T) {
	if pts := DayBucket(nil); pts != nil {
		t.Fatalf("DayBucket(nil) = %+v, want nil", pts)
	}
}

// TestLinearFitShortSeries: 0- and 1-point series must refuse to fit —
// OK=false, no panic, no fake precision.
func TestLinearFitShortSeries(t *testing.T) {
	cases := []struct {
		name string
		pts  []Point
	}{
		{"zero points", nil},
		{"one point", []Point{{Day: day(0), Bytes: 100}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := LinearFit(tc.pts)
			if f.OK {
				t.Fatalf("LinearFit(%d pts).OK = true, want false", len(tc.pts))
			}
			if f.BytesPerDay != 0 {
				t.Fatalf("not-OK fit leaked a rate: %v", f.BytesPerDay)
			}
		})
	}
}

func TestLinearFitTwoPoints(t *testing.T) {
	// 100 MB on day 0, 300 MB on day 2 -> exactly 100 MB/day over 2 days.
	f := LinearFit([]Point{
		{Day: day(0), Bytes: 100_000_000},
		{Day: day(2), Bytes: 300_000_000},
	})
	if !f.OK {
		t.Fatal("fit not OK for a 2-point series")
	}
	if math.Abs(f.BytesPerDay-100_000_000) > 1 {
		t.Errorf("BytesPerDay = %v, want 100000000", f.BytesPerDay)
	}
	if f.WindowDays != 2 {
		t.Errorf("WindowDays = %d, want 2", f.WindowDays)
	}
	if f.Points != 2 {
		t.Errorf("Points = %d, want 2", f.Points)
	}
}

func TestLinearFitExactLine(t *testing.T) {
	// Perfect line: 10 + 5x over 5 days. Slope must be exact.
	var pts []Point
	for i := 0; i < 5; i++ {
		pts = append(pts, Point{Day: day(i), Bytes: int64(10 + 5*i)})
	}
	f := LinearFit(pts)
	if !f.OK || math.Abs(f.BytesPerDay-5) > 1e-9 {
		t.Fatalf("fit = %+v, want slope 5", f)
	}
	if f.WindowDays != 4 {
		t.Errorf("WindowDays = %d, want 4", f.WindowDays)
	}
}

func TestLinearFitShrinkingSeries(t *testing.T) {
	f := LinearFit([]Point{
		{Day: day(0), Bytes: 500},
		{Day: day(1), Bytes: 400},
		{Day: day(2), Bytes: 300},
	})
	if !f.OK || f.BytesPerDay >= 0 {
		t.Fatalf("fit = %+v, want negative slope", f)
	}
}

func TestDaysUntilFull(t *testing.T) {
	cases := []struct {
		name     string
		free     int64
		rate     float64
		wantDays float64
		wantOK   bool
	}{
		{"filling", 10_000_000_000, 1_000_000_000, 10, true},
		{"shrinking", 10_000_000_000, -5, 0, false},
		{"flat", 10_000_000_000, 0, 0, false},
		{"already full", 0, 100, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, ok := DaysUntilFull(tc.free, tc.rate)
			if ok != tc.wantOK || math.Abs(d-tc.wantDays) > 1e-9 {
				t.Fatalf("DaysUntilFull(%d, %v) = (%v, %v), want (%v, %v)",
					tc.free, tc.rate, d, ok, tc.wantDays, tc.wantOK)
			}
		})
	}
}

func TestBatches(t *testing.T) {
	gap := 2 * time.Minute
	base := day(0)
	cases := []struct {
		name  string
		times []time.Time
		want  int
	}{
		{"empty", nil, 0},
		{"single", []time.Time{base}, 1},
		{"one batch, many targets", []time.Time{
			base, base.Add(time.Second), base.Add(2 * time.Second),
		}, 1},
		{"two scans an hour apart", []time.Time{
			base, base.Add(time.Second), base.Add(time.Hour), base.Add(time.Hour + time.Second),
		}, 2},
		{"unsorted input", []time.Time{
			base.Add(time.Hour), base, base.Add(time.Second),
		}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Batches(tc.times, gap); got != tc.want {
				t.Fatalf("Batches = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestRecurrences is the acceptance-criterion fixture: one signature on two
// distinct days IS a pattern (carrying the workaround verbatim); a signature
// seen once — or many times within a single day — is NOT.
func TestRecurrences(t *testing.T) {
	const chromeFix = "chrome --disable-features=MacAppCodeSignClone"
	occ := []Occurrence{
		// chrome clone leak: seen on day 0 and day 7 -> recurring.
		{Signature: "chrome-code-sign-clone", Workaround: chromeFix,
			Target: "/private/var/folders/x/a.code_sign_clone", At: day(0).Add(9 * time.Hour)},
		{Signature: "chrome-code-sign-clone", Workaround: chromeFix,
			Target: "/private/var/folders/x/b.code_sign_clone", At: day(7).Add(11 * time.Hour)},
		// scratch leak: seen twice but on the SAME day -> a burst, not a pattern.
		{Signature: "private-tmp-agent-scratch", Workaround: "",
			Target: "/private/tmp/claude-501", At: day(3).Add(1 * time.Hour)},
		{Signature: "private-tmp-agent-scratch", Workaround: "",
			Target: "/private/tmp/claude-502", At: day(3).Add(20 * time.Hour)},
		// one-off -> not a pattern.
		{Signature: "shipit-updater", Workaround: "safe to delete", Target: "/x", At: day(5)},
		// unclassified suggestion (no signature) -> ignored entirely.
		{Signature: "", Workaround: "", Target: "/y", At: day(1)},
	}

	pats := Recurrences(occ)
	if len(pats) != 1 {
		t.Fatalf("Recurrences = %d patterns (%+v), want exactly 1", len(pats), pats)
	}
	p := pats[0]
	if p.Signature != "chrome-code-sign-clone" {
		t.Errorf("Signature = %q", p.Signature)
	}
	if p.Workaround != chromeFix {
		t.Errorf("Workaround = %q, want verbatim %q", p.Workaround, chromeFix)
	}
	if p.DistinctDays != 2 || p.Occurrences != 2 {
		t.Errorf("DistinctDays=%d Occurrences=%d, want 2/2", p.DistinctDays, p.Occurrences)
	}
	if len(p.Targets) != 2 {
		t.Errorf("Targets = %v, want both unique targets", p.Targets)
	}
	if !p.FirstSeen.Equal(day(0).Add(9*time.Hour)) || !p.LastSeen.Equal(day(7).Add(11*time.Hour)) {
		t.Errorf("First/Last = %v / %v", p.FirstSeen, p.LastSeen)
	}
}

func TestRecurrencesOrdering(t *testing.T) {
	occ := []Occurrence{
		{Signature: "b-three-days", At: day(0)},
		{Signature: "b-three-days", At: day(1)},
		{Signature: "b-three-days", At: day(2)},
		{Signature: "a-two-days", At: day(0)},
		{Signature: "a-two-days", At: day(1)},
	}
	pats := Recurrences(occ)
	if len(pats) != 2 || pats[0].Signature != "b-three-days" || pats[1].Signature != "a-two-days" {
		t.Fatalf("ordering wrong: %+v", pats)
	}
}

func TestSparkline(t *testing.T) {
	cases := []struct {
		name   string
		values []int64
		want   string
	}{
		{"empty", nil, ""},
		{"flat is mid, not empty", []int64{5, 5, 5}, "▄▄▄"},
		{"ramp", []int64{0, 1, 2, 3, 4, 5, 6, 7}, "▁▂▃▄▅▆▇█"},
		{"min max", []int64{10, 90}, "▁█"},
		{"single value", []int64{42}, "▄"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Sparkline(tc.values); got != tc.want {
				t.Fatalf("Sparkline(%v) = %q, want %q", tc.values, got, tc.want)
			}
		})
	}
}
