// `noo-noo trends` — the patterns, literally visible: per-target growth
// sparklines over the daemon's recorded history, a cited-window linear
// forecast, and recurrence detection over persisted suggestions ("this leak
// class keeps coming back — fix it permanently").
//
// All math lives in internal/trend (pure, table-tested); this file only
// adapts store rows into trend types and renders. Reads are in-process
// against the daemon's SQLite store (charter D18: no disk data crosses IPC;
// WAL + busy_timeout make a concurrent CLI read safe).

package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/FRIKKern/noo-noo/internal/config"
	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/store"
	"github.com/FRIKKern/noo-noo/internal/trend"
)

func init() { Register("trends", trendsCmd) }

// recurrenceWindowDays is how far back recurrence detection looks. Wider than
// the growth window on purpose: a leak that re-appears every few weeks is
// exactly the pattern worth surfacing.
const recurrenceWindowDays = 90

// cacheTrend is one target's day-bucketed growth story.
type cacheTrend struct {
	Target   string
	Buckets  []trend.Point
	Fit      trend.Fit
	NowBytes int64
}

// diskTrend is one volume's fill story: the fit runs over USED bytes
// (total-free), so a positive slope means "filling".
type diskTrend struct {
	UUID          string
	MountPoint    string
	TotalBytes    int64
	FreeBytes     int64
	Buckets       []trend.Point
	Fit           trend.Fit
	DaysUntilFull float64
	FullOK        bool
}

// trendsData is everything the render layer needs; gatherTrends fills it.
type trendsData struct {
	WindowDays     int
	RecurrenceDays int
	Caches         []cacheTrend
	Disks          []diskTrend
	Patterns       []trend.Pattern
}

func trendsCmd(_ context.Context, app *App, args []string) int {
	fs := flag.NewFlagSet("trends", flag.ContinueOnError)
	fs.SetOutput(app.Err)
	asJSON := fs.Bool("json", false, "output NDJSON")
	days := fs.Int("days", 30, "growth window in days")
	storeOverride := fs.String("store", "", "path to the daemon store (default: configured store_path)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// Verb-safety (charter D22 / the -y trust bug): this command has no
	// verbs, so ANY positional is a mistake — and flag.Parse stops at the
	// first one, silently swallowing whatever follows. Hard-error instead.
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(app.Err, "trends takes no arguments (got %q) — flags only: -days N -json -store PATH\n", fs.Args())
		return 2
	}
	if *days < 1 {
		_, _ = fmt.Fprintln(app.Err, "-days must be >= 1")
		return 2
	}

	st, path, err := openDaemonStore(*storeOverride)
	if err != nil {
		_, _ = fmt.Fprintln(app.Err, err)
		return 1
	}
	defer func() { _ = st.Close() }()

	data, err := gatherTrends(st, time.Now(), *days)
	if err != nil {
		_, _ = fmt.Fprintf(app.Err, "read %s: %v\n", path, err)
		return 1
	}
	if *asJSON {
		if err := renderTrendsJSON(app.Out, data); err != nil {
			_, _ = fmt.Fprintln(app.Err, err)
			return 1
		}
		return 0
	}
	renderTrends(app.Out, data)
	return 0
}

// openDaemonStore opens the daemon's SQLite store, defaulting to the
// configured store_path. A missing store is an honest, actionable error, not
// an empty render pretending the daemon has been watching.
func openDaemonStore(override string) (*store.Store, string, error) {
	path := override
	if path == "" {
		cfg, err := config.Load(defaultConfigPath())
		if err != nil {
			cfg = config.Defaults()
		}
		path = cfg.Daemon.StorePath
	}
	if _, err := os.Stat(path); err != nil {
		return nil, path, fmt.Errorf("no daemon store at %s — the daemon records the history this command reads; install it with `noo-noo install`, or point -store at a store file", path)
	}
	st, err := store.Open(path)
	if err != nil {
		return nil, path, fmt.Errorf("open daemon store %s: %w", path, err)
	}
	return st, path, nil
}

// gatherTrends adapts store rows into trend math. Every series is
// day-bucketed FIRST: the daemon's history mixes a ~5-minute pressure
// cadence with the daily tick, and a raw fit over that mix is garbage.
func gatherTrends(st *store.Store, now time.Time, days int) (trendsData, error) {
	since := now.Add(-time.Duration(days) * 24 * time.Hour)
	data := trendsData{WindowDays: days, RecurrenceDays: recurrenceWindowDays}

	targets, err := distinctColumn(st, `SELECT DISTINCT target_path FROM cache_size_history ORDER BY target_path`)
	if err != nil {
		return data, err
	}
	for _, target := range targets {
		series, err := st.CacheSizeSeries(target, since)
		if err != nil {
			return data, err
		}
		if len(series) == 0 {
			continue
		}
		samples := make([]trend.Sample, len(series))
		for i, s := range series {
			samples[i] = trend.Sample{At: s.At, Bytes: s.Bytes}
		}
		buckets := trend.DayBucket(samples)
		data.Caches = append(data.Caches, cacheTrend{
			Target:   target,
			Buckets:  buckets,
			Fit:      trend.LinearFit(buckets),
			NowBytes: series[len(series)-1].Bytes,
		})
	}

	volumes, err := distinctColumn(st, `SELECT DISTINCT volume_uuid FROM disk_space_history ORDER BY volume_uuid`)
	if err != nil {
		return data, err
	}
	for _, uuid := range volumes {
		series, err := st.DiskSpaceSeries(uuid, since)
		if err != nil {
			return data, err
		}
		if len(series) == 0 {
			continue
		}
		latest := series[len(series)-1]
		samples := make([]trend.Sample, len(series))
		for i, s := range series {
			samples[i] = trend.Sample{At: s.At, Bytes: s.TotalBytes - s.FreeBytes} // used
		}
		buckets := trend.DayBucket(samples)
		fit := trend.LinearFit(buckets)
		dt := diskTrend{
			UUID:       uuid,
			MountPoint: latest.MountPoint,
			TotalBytes: latest.TotalBytes,
			FreeBytes:  latest.FreeBytes,
			Buckets:    buckets,
			Fit:        fit,
		}
		if fit.OK {
			dt.DaysUntilFull, dt.FullOK = trend.DaysUntilFull(latest.FreeBytes, fit.BytesPerDay)
		}
		data.Disks = append(data.Disks, dt)
	}

	patterns, err := gatherRecurrences(st, now, recurrenceWindowDays)
	if err != nil {
		return data, err
	}
	data.Patterns = patterns
	return data, nil
}

// gatherRecurrences reads persisted suggestions — INCLUDING dismissed ones,
// because "surfaced, cleaned, came back" is exactly the recurrence signal —
// and groups them by leak-class signature.
func gatherRecurrences(st *store.Store, now time.Time, windowDays int) ([]trend.Pattern, error) {
	since := now.Add(-time.Duration(windowDays) * 24 * time.Hour)
	modules, err := distinctColumn(st, `SELECT DISTINCT module FROM suggestions ORDER BY module`)
	if err != nil {
		return nil, err
	}
	var occ []trend.Occurrence
	for _, mod := range modules {
		rows, err := st.ListSuggestionsSince(mod, since)
		if err != nil {
			return nil, err
		}
		for _, sg := range rows {
			occ = append(occ, trend.Occurrence{
				Signature:  sg.Evidence["signature"],
				Workaround: sg.Evidence["workaround"],
				Target:     sg.Target,
				At:         sg.Ts,
			})
		}
	}
	return trend.Recurrences(occ), nil
}

// distinctColumn runs a single-column query. Same direct-SQL pattern as
// heuristics.distinctCacheTargets: no store-surface widening for a private
// listing query.
func distinctColumn(st *store.Store, query string) ([]string, error) {
	rows, err := st.DB().Query(query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// bucketValues projects day-buckets to their byte values for sparklining.
func bucketValues(pts []trend.Point) []int64 {
	out := make([]int64, len(pts))
	for i, p := range pts {
		out[i] = p.Bytes
	}
	return out
}

// formatRate renders a signed bytes/day rate, e.g. "+143.0 MB/day".
func formatRate(bytesPerDay float64) string {
	sign := "+"
	if bytesPerDay < 0 {
		sign = "-"
		bytesPerDay = -bytesPerDay
	}
	return sign + core.Bytes(int64(bytesPerDay)).String() + "/day"
}

// formatFullIn renders a days-until-full projection honestly: "not filling"
// when the disk is shrinking or flat, "~N days" when it is.
func formatFullIn(days float64, ok bool) string {
	if !ok {
		return "not filling"
	}
	if days > 3650 {
		return ">10 years"
	}
	return fmt.Sprintf("~%.0f days", days)
}

// daysWord pluralizes a cited day count.
func daysWord(n int) string {
	if n == 1 {
		return "day"
	}
	return "days"
}

// insufficientHistory is the honest short-series fallback, cited with what
// we actually have.
func insufficientHistory(buckets int) string {
	return fmt.Sprintf("insufficient history (%d %s) — check back tomorrow", buckets, daysWord(buckets))
}

func renderTrends(out io.Writer, d trendsData) {
	_, _ = fmt.Fprintf(out, "Cache growth — last %d days, day-bucketed (last sample per day)\n", d.WindowDays)
	if len(d.Caches) == 0 {
		_, _ = fmt.Fprintln(out, "  No cache history yet — the daemon records sizes on each scan; check back after a day of uptime.")
	} else {
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "  TARGET\tTREND\tRATE\tNOW")
		for _, c := range d.Caches {
			rate := insufficientHistory(len(c.Buckets))
			if c.Fit.OK {
				rate = fmt.Sprintf("%s over %d %s", formatRate(c.Fit.BytesPerDay), c.Fit.WindowDays, daysWord(c.Fit.WindowDays))
			}
			_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n",
				c.Target, trend.Sparkline(bucketValues(c.Buckets)), rate, core.Bytes(c.NowBytes))
		}
		_ = tw.Flush()
	}

	_, _ = fmt.Fprintf(out, "\nDisk fill — last %d days (used bytes, day-bucketed)\n", d.WindowDays)
	if len(d.Disks) == 0 {
		_, _ = fmt.Fprintln(out, "  No disk-space history yet — the daemon samples each volume hourly; check back after a day of uptime.")
	} else {
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "  VOLUME\tTREND\tFILL RATE\tFREE\tFULL IN")
		for _, v := range d.Disks {
			rate := insufficientHistory(len(v.Buckets))
			fullIn := "unknown"
			if v.Fit.OK {
				rate = fmt.Sprintf("%s over %d %s", formatRate(v.Fit.BytesPerDay), v.Fit.WindowDays, daysWord(v.Fit.WindowDays))
				fullIn = formatFullIn(v.DaysUntilFull, v.FullOK)
			}
			_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n",
				v.MountPoint, trend.Sparkline(bucketValues(v.Buckets)), rate, core.Bytes(v.FreeBytes), fullIn)
		}
		_ = tw.Flush()
	}

	_, _ = fmt.Fprintf(out, "\nRecurring patterns — last %d days (dismissed suggestions included)\n", d.RecurrenceDays)
	if len(d.Patterns) == 0 {
		_, _ = fmt.Fprintln(out, "  No recurring patterns — no leak class has come back on a second day.")
		return
	}
	for _, p := range d.Patterns {
		_, _ = fmt.Fprintf(out, "  %s — seen on %d distinct days (%d hits; first %s, last %s)\n",
			p.Signature, p.DistinctDays, p.Occurrences,
			p.FirstSeen.Format("2006-01-02"), p.LastSeen.Format("2006-01-02"))
		if p.Workaround != "" {
			_, _ = fmt.Fprintf(out, "    recurring — fix it permanently: %s\n", p.Workaround)
		} else {
			_, _ = fmt.Fprintln(out, "    recurring — no known permanent fix yet; expect to clean this again")
		}
	}
}

// trendsJSONRow types keep the NDJSON stream self-describing via "kind".
type cacheTrendJSON struct {
	Kind        string  `json:"kind"`
	Target      string  `json:"target"`
	Sparkline   string  `json:"sparkline"`
	FitOK       bool    `json:"fit_ok"`
	BytesPerDay float64 `json:"bytes_per_day"`
	WindowDays  int     `json:"window_days"`
	Points      int     `json:"points"`
	NowBytes    int64   `json:"now_bytes"`
}

type diskTrendJSON struct {
	Kind          string  `json:"kind"`
	VolumeUUID    string  `json:"volume_uuid"`
	MountPoint    string  `json:"mount_point"`
	TotalBytes    int64   `json:"total_bytes"`
	FreeBytes     int64   `json:"free_bytes"`
	Sparkline     string  `json:"sparkline"`
	FitOK         bool    `json:"fit_ok"`
	BytesPerDay   float64 `json:"bytes_per_day"`
	WindowDays    int     `json:"window_days"`
	Points        int     `json:"points"`
	DaysUntilFull float64 `json:"days_until_full"`
	FullOK        bool    `json:"full_ok"`
}

type patternJSON struct {
	Kind         string   `json:"kind"`
	Signature    string   `json:"signature"`
	DistinctDays int      `json:"distinct_days"`
	Occurrences  int      `json:"occurrences"`
	FirstSeen    string   `json:"first_seen"`
	LastSeen     string   `json:"last_seen"`
	Workaround   string   `json:"workaround,omitempty"`
	Targets      []string `json:"targets,omitempty"`
}

func renderTrendsJSON(out io.Writer, d trendsData) error {
	enc := json.NewEncoder(out)
	for _, c := range d.Caches {
		row := cacheTrendJSON{
			Kind: "cache_trend", Target: c.Target,
			Sparkline: trend.Sparkline(bucketValues(c.Buckets)),
			FitOK:     c.Fit.OK, BytesPerDay: c.Fit.BytesPerDay,
			WindowDays: c.Fit.WindowDays, Points: len(c.Buckets), NowBytes: c.NowBytes,
		}
		if err := enc.Encode(row); err != nil {
			return err
		}
	}
	for _, v := range d.Disks {
		row := diskTrendJSON{
			Kind: "disk_trend", VolumeUUID: v.UUID, MountPoint: v.MountPoint,
			TotalBytes: v.TotalBytes, FreeBytes: v.FreeBytes,
			Sparkline: trend.Sparkline(bucketValues(v.Buckets)),
			FitOK:     v.Fit.OK, BytesPerDay: v.Fit.BytesPerDay,
			WindowDays: v.Fit.WindowDays, Points: len(v.Buckets),
			DaysUntilFull: v.DaysUntilFull, FullOK: v.FullOK,
		}
		if err := enc.Encode(row); err != nil {
			return err
		}
	}
	for _, p := range d.Patterns {
		row := patternJSON{
			Kind: "recurring_pattern", Signature: p.Signature,
			DistinctDays: p.DistinctDays, Occurrences: p.Occurrences,
			FirstSeen: p.FirstSeen.Format(time.RFC3339), LastSeen: p.LastSeen.Format(time.RFC3339),
			Workaround: p.Workaround, Targets: p.Targets,
		}
		if err := enc.Encode(row); err != nil {
			return err
		}
	}
	return nil
}
