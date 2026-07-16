// Package trend is the pure math behind `noo-noo status` and `noo-noo
// trends`: day-bucketing of mixed-cadence time series, least-squares growth
// fitting, days-until-full projection, scan-batch density, and recurrence
// detection over persisted suggestions.
//
// The package is deliberately I/O-free — no store, no statfs, no clock — so
// every function is table-testable. Callers (the CLI commands) adapt store
// rows into these types.
//
// Why day-bucket first: the daemon's series mix a ~5-minute pressure-trigger
// cadence with the daily tick (proven on the reference machine — pressure
// samples outnumber daily ones by ~2 orders of magnitude). A raw
// least-squares fit over that mix is dominated by whichever burst happened
// last; collapsing to one point per UTC day first makes the fit honest.
package trend

import (
	"math"
	"sort"
	"time"
)

// Sample is one raw time-series observation (a cache_size_history or
// disk_space_history row).
type Sample struct {
	At    time.Time
	Bytes int64
}

// Point is one day-bucketed observation. Day is midnight UTC of the bucket.
type Point struct {
	Day   time.Time
	Bytes int64
}

// DayBucket collapses raw samples into one Point per UTC day, keeping the
// LAST sample of each day (the end-of-day state — for a size series that is
// the honest daily value). Input order does not matter; output is sorted by
// day ascending.
func DayBucket(samples []Sample) []Point {
	if len(samples) == 0 {
		return nil
	}
	sorted := make([]Sample, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })

	byDay := map[time.Time]int64{}
	for _, s := range sorted {
		day := s.At.UTC().Truncate(24 * time.Hour)
		byDay[day] = s.Bytes // later samples overwrite: last-of-day wins
	}
	out := make([]Point, 0, len(byDay))
	for day, b := range byDay {
		out = append(out, Point{Day: day, Bytes: b})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day.Before(out[j].Day) })
	return out
}

// Fit is a least-squares linear fit over day-bucketed points.
type Fit struct {
	// OK is false when the series is too short to fit (fewer than two
	// day-buckets). Callers MUST degrade honestly instead of inventing a
	// rate — never render a projection off a not-OK fit.
	OK bool
	// BytesPerDay is the slope: positive = filling, negative = shrinking.
	BytesPerDay float64
	// WindowDays is the span between the first and last bucket, in days
	// (>= 1 when OK). This is the citation: "over the last N days".
	WindowDays int
	// Points is how many day-buckets the fit saw.
	Points int
}

// LinearFit runs ordinary least squares over day-bucketed points, with the
// x-axis in days since the first bucket. Fewer than two points returns
// OK=false — one observation has no direction, and pretending otherwise is
// fake precision.
func LinearFit(pts []Point) Fit {
	if len(pts) < 2 {
		return Fit{OK: false, Points: len(pts)}
	}
	x0 := pts[0].Day
	var sumX, sumY, sumXY, sumXX float64
	n := float64(len(pts))
	for _, p := range pts {
		x := p.Day.Sub(x0).Hours() / 24
		y := float64(p.Bytes)
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}
	denom := n*sumXX - sumX*sumX
	if denom == 0 {
		// All buckets on the same day — cannot happen after DayBucket
		// (distinct days by construction), but guard anyway.
		return Fit{OK: false, Points: len(pts)}
	}
	slope := (n*sumXY - sumX*sumY) / denom
	window := int(math.Round(pts[len(pts)-1].Day.Sub(x0).Hours() / 24))
	if window < 1 {
		window = 1
	}
	return Fit{OK: true, BytesPerDay: slope, WindowDays: window, Points: len(pts)}
}

// DaysUntilFull projects when freeBytes reaches zero at bytesPerDay. The
// second return is false when the disk is not filling (rate <= 0) or free
// space is already gone/unknown (freeBytes <= 0 with a positive rate still
// returns 0, true — "full now").
func DaysUntilFull(freeBytes int64, bytesPerDay float64) (float64, bool) {
	if bytesPerDay <= 0 {
		return 0, false
	}
	if freeBytes <= 0 {
		return 0, true
	}
	return float64(freeBytes) / bytesPerDay, true
}

// Batches counts scan batches in a timestamp series: consecutive timestamps
// within gap of each other collapse into one batch. The daemon records many
// targets per scan with near-identical timestamps; batch count approximates
// "how many times did a scan trigger fire", which is the pressure-posture
// signal (a daily tick alone is 1 batch/day; a machine living at the
// memory-pressure threshold shows hundreds).
func Batches(times []time.Time, gap time.Duration) int {
	if len(times) == 0 {
		return 0
	}
	sorted := make([]time.Time, len(times))
	copy(sorted, times)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Before(sorted[j]) })
	batches := 1
	last := sorted[0]
	for _, t := range sorted[1:] {
		if t.Sub(last) > gap {
			batches++
		}
		last = t
	}
	return batches
}

// Occurrence is one persisted suggestion projected down to what recurrence
// detection needs: which leak-class signature fired, when, on what, and the
// signature's permanent remediation.
type Occurrence struct {
	Signature  string
	Workaround string
	Target     string
	At         time.Time
}

// Pattern is a signature that keeps coming back: seen on two or more
// distinct UTC days. That is no longer a one-off cleanup — it deserves the
// permanent remediation (Workaround) front and center.
type Pattern struct {
	Signature    string
	Workaround   string
	DistinctDays int
	Occurrences  int
	FirstSeen    time.Time
	LastSeen     time.Time
	Targets      []string // unique targets, insertion-ordered
}

// Recurrences groups occurrences by signature and returns the ones seen on
// >= 2 distinct UTC days, most-recurrent first (then by signature for a
// stable order). Occurrences with an empty signature are ignored — they
// cannot be grouped into a class. A signature seen many times on ONE day is
// a burst, not a recurrence, and is excluded by design.
func Recurrences(occ []Occurrence) []Pattern {
	type agg struct {
		p    Pattern
		days map[time.Time]struct{}
		seen map[string]struct{}
	}
	groups := map[string]*agg{}
	var order []string
	for _, o := range occ {
		if o.Signature == "" {
			continue
		}
		g, ok := groups[o.Signature]
		if !ok {
			g = &agg{
				p:    Pattern{Signature: o.Signature, FirstSeen: o.At, LastSeen: o.At},
				days: map[time.Time]struct{}{},
				seen: map[string]struct{}{},
			}
			groups[o.Signature] = g
			order = append(order, o.Signature)
		}
		g.days[o.At.UTC().Truncate(24*time.Hour)] = struct{}{}
		g.p.Occurrences++
		if o.At.Before(g.p.FirstSeen) {
			g.p.FirstSeen = o.At
		}
		if o.At.After(g.p.LastSeen) {
			g.p.LastSeen = o.At
		}
		if o.Workaround != "" {
			g.p.Workaround = o.Workaround
		}
		if o.Target != "" {
			if _, dup := g.seen[o.Target]; !dup {
				g.seen[o.Target] = struct{}{}
				g.p.Targets = append(g.p.Targets, o.Target)
			}
		}
	}
	var out []Pattern
	for _, sig := range order {
		g := groups[sig]
		if len(g.days) < 2 {
			continue
		}
		g.p.DistinctDays = len(g.days)
		out = append(out, g.p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].DistinctDays != out[j].DistinctDays {
			return out[i].DistinctDays > out[j].DistinctDays
		}
		return out[i].Signature < out[j].Signature
	})
	return out
}
