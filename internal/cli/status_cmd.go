// `noo-noo status` — the machine's storage posture in one honest statement:
// how big and how full the internal disk is, how fast it is REALLY filling
// (day-bucketed fit over recorded history, window cited), what external
// headroom exists and whether each volume is actually usable, whether offload
// is configured and live-verified, and how hard the pressure watcher is
// working. Closes with the verdict the user asked for verbatim: "stuck on
// one small disk" vs "you have external headroom".
//
// Live reads (statfs, diskutil) happen in THIS process — charter D18: the
// IPC surface has no disk data and gets no new methods this wave. History
// reads go straight to the daemon's SQLite store (WAL-safe).

package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/FRIKKern/noo-noo/internal/config"
	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/store"
	"github.com/FRIKKern/noo-noo/internal/trend"
	"github.com/FRIKKern/noo-noo/internal/vmdisk"
)

func init() { Register("status", statusCmd) }

// nearContinuousBatches24h is the pressure-posture threshold: the daily tick
// alone is 1 scan batch/day and an hourly cadence is 24; at the ~5-minute
// pressure cooldown the ceiling is ~288. 100+ batches means pressure
// triggers drove scanning for most of the day — a designed signal that the
// machine lives at its thresholds (charter D18), worth saying out loud.
const nearContinuousBatches24h = 100

// internalPosture is the boot volume's story.
type internalPosture struct {
	MountPoint    string
	TotalBytes    int64
	FreeBytes     int64
	Fit           trend.Fit // over USED bytes, day-bucketed
	HistoryDays   int       // day-buckets actually observed in the window
	DaysUntilFull float64
	FullOK        bool
}

// offloadPosture reports whether relocation has somewhere real to go.
type offloadPosture struct {
	Configured bool
	DestRoot   string
	Usable     bool
	Detail     string // guard verdict, human-phrased
}

// pressurePosture is scan-trigger density over the last 24h.
type pressurePosture struct {
	Batches24h     int
	NearContinuous bool
}

// statusData is everything the render layer needs. gatherStatus fills it
// from live statfs/diskutil + the store; tests build it directly.
type statusData struct {
	WindowDays    int
	Internal      internalPosture
	CacheFallback []cacheTrend // top growing caches, shown when disk history is short
	Externals     []core.Volume
	Offload       offloadPosture
	VM            vmdisk.Report // guest datadisk posture; empty = silent section
	Pressure      pressurePosture
	Memory        core.MemorySnapshot // live RAM/swap/compressor posture
	MemoryOK      bool                // false when even physical RAM could not be read
	MemoryNote    string              // why, when !MemoryOK
	StoreNote     string              // non-empty when history could not be read (missing store etc.)
}

// statusGatherFn is the gather hook; tests override it to inject fixtures
// (same pattern as autoCleanDial).
var statusGatherFn = gatherStatus

func statusCmd(ctx context.Context, app *App, args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(app.Err)
	asJSON := fs.Bool("json", false, "output JSON")
	days := fs.Int("days", 30, "history window for the fill-rate fit, in days")
	storeOverride := fs.String("store", "", "path to the daemon store (default: configured store_path)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// Verb-safety (charter D22): status has no verbs; a positional means the
	// user's flags after it would be silently swallowed. Hard-error.
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(app.Err, "status takes no arguments (got %q) — flags only: -days N -json -store PATH\n", fs.Args())
		return 2
	}
	if *days < 1 {
		_, _ = fmt.Fprintln(app.Err, "-days must be >= 1")
		return 2
	}

	data, err := statusGatherFn(ctx, *storeOverride, *days, time.Now())
	if err != nil {
		_, _ = fmt.Fprintln(app.Err, err)
		return 1
	}
	if *asJSON {
		if err := renderStatusJSON(app.Out, data); err != nil {
			_, _ = fmt.Fprintln(app.Err, err)
			return 1
		}
		return 0
	}
	renderStatus(app.Out, data)
	return 0
}

// gatherStatus assembles the live posture. The internal disk's total/free
// always come from a live statfs — they must be true even when the daemon
// has never run; history-derived parts degrade honestly via StoreNote.
func gatherStatus(ctx context.Context, storeOverride string, days int, now time.Time) (statusData, error) {
	data := statusData{WindowDays: days}

	total, free, err := core.VolumeCapacity("/")
	if err != nil {
		return data, fmt.Errorf("statfs /: %w", err)
	}
	data.Internal = internalPosture{MountPoint: "/", TotalBytes: total, FreeBytes: free}

	vols, err := core.ListVolumes(ctx, nil)
	if err != nil {
		// diskutil failing is worth reporting, but the internal statfs and
		// history posture are still honest without it.
		data.StoreNote = appendNote(data.StoreNote, fmt.Sprintf("volume inventory unavailable: %v", err))
	}
	var internalUUID string
	for _, v := range vols {
		if v.MountPoint == "/" {
			internalUUID = v.UUID
			continue
		}
		data.Externals = append(data.Externals, v)
	}

	// Offload posture: configured? Then prove it live (UUID + write-probe).
	cfg, err := config.Load(defaultConfigPath())
	if err != nil {
		cfg = config.Defaults()
	}
	data.Offload = checkOffloadPosture(ctx, cfg)

	// Memory posture: the founding Jetsam day was a memory problem the disk
	// lines could not see. Three tolerated shell-outs; only a failed
	// physical-RAM read blanks the section.
	if m, err := core.ReadMemory(ctx); err == nil {
		data.Memory, data.MemoryOK = m, true
	} else {
		data.MemoryNote = err.Error()
	}

	// VM inner disks (charter D21): a guest datadisk can hit 100% and kill
	// dockerd while the HOST still reports plenty of free space, so posture
	// must look inside. Read-only and silent when no VM manager is installed;
	// a detection failure is a posture note, never a status failure.
	if vmRep, err := (&vmdisk.Detector{}).Report(ctx, cfg.Offload.DestRoot); err == nil {
		data.VM = vmRep
	} else {
		data.StoreNote = appendNote(data.StoreNote, fmt.Sprintf("vm datadisk inventory unavailable: %v", err))
	}

	// History-derived posture. A missing store degrades to "no history",
	// never to a lie.
	st, path, err := openDaemonStore(storeOverride)
	if err != nil {
		data.StoreNote = appendNote(data.StoreNote, err.Error())
		return data, nil
	}
	defer func() { _ = st.Close() }()

	since := now.Add(-time.Duration(days) * 24 * time.Hour)
	if internalUUID != "" {
		if err := fillInternalTrend(&data, st, internalUUID, free, since); err != nil {
			return data, fmt.Errorf("read %s: %w", path, err)
		}
	}

	// When disk history can't carry a fit yet, fall back to cache velocity:
	// the fastest-growing recorded targets, day-bucketed the same way.
	if !data.Internal.Fit.OK {
		if err := fillCacheFallback(&data, st, now, days); err != nil {
			return data, fmt.Errorf("read %s: %w", path, err)
		}
	}

	// Pressure posture: scan batches over the last 24h across both history
	// tables. Timestamps within 2 minutes collapse into one batch (one scan
	// records many targets back-to-back).
	times, err := scanTimestamps(st, now.Add(-24*time.Hour))
	if err != nil {
		return data, fmt.Errorf("read %s: %w", path, err)
	}
	batches := trend.Batches(times, 2*time.Minute)
	data.Pressure = pressurePosture{
		Batches24h:     batches,
		NearContinuous: batches >= nearContinuousBatches24h,
	}
	return data, nil
}

// fillInternalTrend fits the internal disk's usage history into data.Internal
// and, when the fit holds, projects days-until-full from the current free
// bytes. Split out of gatherStatus to keep the assembler flat.
func fillInternalTrend(data *statusData, st *store.Store, internalUUID string, free int64, since time.Time) error {
	series, err := st.DiskSpaceSeries(internalUUID, since)
	if err != nil {
		return err
	}
	samples := make([]trend.Sample, len(series))
	for i, s := range series {
		samples[i] = trend.Sample{At: s.At, Bytes: s.TotalBytes - s.FreeBytes}
	}
	buckets := trend.DayBucket(samples)
	data.Internal.HistoryDays = len(buckets)
	data.Internal.Fit = trend.LinearFit(buckets)
	if data.Internal.Fit.OK {
		data.Internal.DaysUntilFull, data.Internal.FullOK =
			trend.DaysUntilFull(free, data.Internal.Fit.BytesPerDay)
	}
	return nil
}

// fillCacheFallback populates data.CacheFallback with the top few
// fastest-growing caches, the fallback posture when the internal disk history
// cannot carry a fit yet.
func fillCacheFallback(data *statusData, st *store.Store, now time.Time, days int) error {
	trendsData, err := gatherTrends(st, now, days)
	if err != nil {
		return err
	}
	for _, c := range trendsData.Caches {
		if c.Fit.OK && c.Fit.BytesPerDay > 0 {
			data.CacheFallback = append(data.CacheFallback, c)
		}
	}
	if len(data.CacheFallback) > 3 {
		data.CacheFallback = data.CacheFallback[:3]
	}
	return nil
}

// checkOffloadPosture live-verifies the configured offload destination with
// the same guard Apply uses: UUID pin + write probe. Unconfigured is not an
// error — it is a posture fact.
func checkOffloadPosture(ctx context.Context, cfg config.Config) offloadPosture {
	if cfg.Offload.DestRoot == "" || cfg.Offload.DestVolumeUUID == "" {
		return offloadPosture{
			Configured: false,
			Detail:     "not configured — set [offload] dest_root + dest_volume_uuid to enable relocation",
		}
	}
	p := offloadPosture{Configured: true, DestRoot: cfg.Offload.DestRoot}
	guardCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := (core.VolGuard{}).CheckDest(guardCtx, cfg.Offload.DestRoot, cfg.Offload.DestVolumeUUID); err != nil {
		p.Usable = false
		p.Detail = fmt.Sprintf("configured but UNUSABLE right now: %v", err)
		return p
	}
	p.Usable = true
	p.Detail = "verified writable just now (UUID-pinned, live write-probe)"
	return p
}

// scanTimestamps collects recorded_at values from both history tables since
// the cutoff — the raw material for the scan-batch density signal. Direct
// SQL by the same precedent as distinctColumn.
func scanTimestamps(st *store.Store, since time.Time) ([]time.Time, error) {
	var out []time.Time
	for _, q := range []string{
		`SELECT recorded_at FROM cache_size_history WHERE recorded_at >= ?`,
		`SELECT recorded_at FROM disk_space_history WHERE recorded_at >= ?`,
	} {
		rows, err := st.DB().Query(q, since.UTC())
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var t time.Time
			if err := rows.Scan(&t); err != nil {
				_ = rows.Close()
				return nil, err
			}
			out = append(out, t)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}
	return out, nil
}

// appendNote joins posture notes with "; ".
func appendNote(existing, note string) string {
	if existing == "" {
		return note
	}
	return existing + "; " + note
}

// verdictSentence is the closing statement — the user's own dichotomy.
// "Stuck" means: no usable external volume exists. Headroom cites the real
// usable bytes; whether offload is configured decides the call to action.
func verdictSentence(internalTotal int64, externals []core.Volume, off offloadPosture) string {
	var headroom int64
	usable := 0
	for _, v := range externals {
		if v.Class == core.VolumeUsable {
			usable++
			headroom += v.FreeBytes
		}
	}
	if usable == 0 {
		return fmt.Sprintf("You are stuck on one small %s disk — no usable external headroom; space must come from cleanup, not offload.",
			core.Bytes(internalTotal))
	}
	if off.Configured && off.Usable {
		return fmt.Sprintf("You have %s of usable external headroom and offload is configured and healthy — offloading is cheaper than deleting.",
			core.Bytes(headroom))
	}
	return fmt.Sprintf("You have %s of usable external headroom — configure [offload] to use it (`noo-noo offload scan` shows what would move).",
		core.Bytes(headroom))
}

// volumeVerdict phrases one external volume's posture with the remediation
// that is actually POSSIBLE for its class (charter D7): media-RO must never
// be told to repair.
func volumeVerdict(v core.Volume) string {
	switch v.Class {
	case core.VolumeMediaRO:
		return "HARDWARE WRITE-LOCKED (media refuses writes) — rescue-copy your data off it, then diagnose the hardware (SD lock switch / failing controller); no software fix can help a write-locked disk"
	case core.VolumeFSCorruptionRO:
		return "read-only from filesystem corruption — back up its contents first, then repair (Disk Utility First Aid)"
	default:
		return fmt.Sprintf("usable — %s free of %s", core.Bytes(v.FreeBytes), core.Bytes(v.TotalBytes))
	}
}

func renderStatus(out io.Writer, d statusData) {
	i := d.Internal
	usedPct := 0.0
	if i.TotalBytes > 0 {
		usedPct = 100 * float64(i.TotalBytes-i.FreeBytes) / float64(i.TotalBytes)
	}
	_, _ = fmt.Fprintf(out, "Internal disk %s\n", i.MountPoint)
	_, _ = fmt.Fprintf(out, "  %s total, %s free (%.0f%% used)\n",
		core.Bytes(i.TotalBytes), core.Bytes(i.FreeBytes), usedPct)
	if i.Fit.OK {
		if i.Fit.BytesPerDay > 0 {
			_, _ = fmt.Fprintf(out, "  filling at %s over the last %d days (%d day-samples) → full in %s\n",
				formatRate(i.Fit.BytesPerDay), i.Fit.WindowDays, i.Fit.Points,
				formatFullIn(i.DaysUntilFull, i.FullOK))
		} else {
			_, _ = fmt.Fprintf(out, "  not filling: %s over the last %d days (%d day-samples)\n",
				formatRate(i.Fit.BytesPerDay), i.Fit.WindowDays, i.Fit.Points)
		}
	} else {
		_, _ = fmt.Fprintf(out, "  fill rate: %s\n", insufficientHistory(i.HistoryDays))
		for _, c := range d.CacheFallback {
			_, _ = fmt.Fprintf(out, "  known grower (cache history): %s %s over %d %s\n",
				c.Target, formatRate(c.Fit.BytesPerDay), c.Fit.WindowDays, daysWord(c.Fit.WindowDays))
		}
	}

	_, _ = fmt.Fprintln(out, "\nMemory")
	renderMemory(out, d)

	_, _ = fmt.Fprintln(out, "\nExternal volumes")
	if len(d.Externals) == 0 {
		_, _ = fmt.Fprintln(out, "  none mounted")
	}
	for _, v := range d.Externals {
		_, _ = fmt.Fprintf(out, "  %s (%s total): %s\n", v.MountPoint, core.Bytes(v.TotalBytes), volumeVerdict(v))
	}

	_, _ = fmt.Fprintln(out, "\nOffload")
	_, _ = fmt.Fprintf(out, "  %s\n", offloadLine(d.Offload))

	// VM datadisk section — silent when no VMs exist (the honest empty state).
	vmdisk.RenderSection(out, d.VM)

	_, _ = fmt.Fprintln(out, "\nPressure")
	if d.Pressure.NearContinuous {
		_, _ = fmt.Fprintf(out, "  scan triggers fired ~%d times in the last 24h — this machine spends much of its day at the memory-pressure threshold (working as designed, but a posture worth knowing)\n",
			d.Pressure.Batches24h)
	} else {
		_, _ = fmt.Fprintf(out, "  %d scan batch(es) in the last 24h — quiet\n", d.Pressure.Batches24h)
	}

	if d.StoreNote != "" {
		_, _ = fmt.Fprintf(out, "\nNote: %s\n", d.StoreNote)
	}

	_, _ = fmt.Fprintf(out, "\nVerdict: %s\n", verdictSentence(i.TotalBytes, d.Externals, d.Offload))
}

// renderMemory prints the memory section: the numbers, the verdict with the
// figures that earned it, and the top processes by compressed memory (the
// ones to quit when the verdict says so). Tolerated read failures become
// notes, never a missing section.
func renderMemory(out io.Writer, d statusData) {
	if !d.MemoryOK {
		_, _ = fmt.Fprintf(out, "  unavailable: %s\n", d.MemoryNote)
		return
	}
	m := d.Memory
	swap := "no swap in use"
	if m.SwapTotalBytes > 0 {
		swap = fmt.Sprintf("swap %s used of %s (%.0f%%)", core.Bytes(m.SwapUsedBytes), core.Bytes(m.SwapTotalBytes), 100*m.SwapFill())
	}
	_, _ = fmt.Fprintf(out, "  %s physical; %s; compressor holds %s (%.0f%% of RAM)\n",
		core.Bytes(m.PhysicalBytes), swap, core.Bytes(m.CompressorBytes), 100*m.CompressorShare())
	_, _ = fmt.Fprintf(out, "  verdict: %s\n", m.VerdictLine())
	if len(m.TopCompressed) > 0 {
		parts := make([]string, 0, len(m.TopCompressed))
		for _, p := range m.TopCompressed {
			parts = append(parts, fmt.Sprintf("%s (pid %d) %s", p.Command, p.PID, core.Bytes(p.CompressedBytes)))
		}
		_, _ = fmt.Fprintf(out, "  top compressed: %s\n", strings.Join(parts, ", "))
	}
	for _, n := range m.Notes {
		_, _ = fmt.Fprintf(out, "  note: %s\n", n)
	}
}

func offloadLine(o offloadPosture) string {
	if !o.Configured {
		return o.Detail
	}
	return fmt.Sprintf("dest %s — %s", o.DestRoot, o.Detail)
}

// statusJSON is the single-object JSON view: status is one statement, not a
// stream.
type statusJSON struct {
	Internal struct {
		MountPoint    string  `json:"mount_point"`
		TotalBytes    int64   `json:"total_bytes"`
		FreeBytes     int64   `json:"free_bytes"`
		FitOK         bool    `json:"fit_ok"`
		BytesPerDay   float64 `json:"bytes_per_day"`
		WindowDays    int     `json:"window_days"`
		HistoryDays   int     `json:"history_days"`
		DaysUntilFull float64 `json:"days_until_full"`
		FullOK        bool    `json:"full_ok"`
	} `json:"internal"`
	Externals []statusVolumeJSON `json:"external_volumes"`
	Offload   struct {
		Configured bool   `json:"configured"`
		DestRoot   string `json:"dest_root,omitempty"`
		Usable     bool   `json:"usable"`
		Detail     string `json:"detail"`
	} `json:"offload"`
	VMs      []statusVMJSON `json:"vm_datadisks,omitempty"`
	Pressure struct {
		Batches24h     int  `json:"scan_batches_24h"`
		NearContinuous bool `json:"near_continuous"`
	} `json:"pressure"`
	Memory  *statusMemoryJSON `json:"memory,omitempty"`
	Note    string            `json:"note,omitempty"`
	Verdict string            `json:"verdict"`
}

// statusMemoryJSON mirrors core.MemorySnapshot plus its verdict.
type statusMemoryJSON struct {
	PhysicalBytes   int64               `json:"physical_bytes"`
	SwapUsedBytes   int64               `json:"swap_used_bytes"`
	SwapTotalBytes  int64               `json:"swap_total_bytes"`
	CompressorBytes int64               `json:"compressor_bytes"`
	CompressorShare float64             `json:"compressor_share"`
	Verdict         string              `json:"verdict"`
	VerdictLine     string              `json:"verdict_line"`
	TopCompressed   []statusMemProcJSON `json:"top_compressed,omitempty"`
	Notes           []string            `json:"notes,omitempty"`
}

type statusMemProcJSON struct {
	PID             int    `json:"pid"`
	Command         string `json:"command"`
	ResidentBytes   int64  `json:"resident_bytes"`
	CompressedBytes int64  `json:"compressed_bytes"`
}

// statusVMJSON is one guest datadisk row, with its advice (when the fill
// crossed the warn threshold) inlined.
type statusVMJSON struct {
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	Running         bool   `json:"running"`
	DatadiskPresent bool   `json:"datadisk_present"`
	TotalBytes      uint64 `json:"total_bytes"`
	UsedBytes       uint64 `json:"used_bytes"`
	PctFull         int    `json:"pct_full"`
	AdviceLevel     string `json:"advice_level,omitempty"`
	Advice          string `json:"advice,omitempty"`
	Command         string `json:"command,omitempty"`
}

type statusVolumeJSON struct {
	MountPoint string `json:"mount_point"`
	UUID       string `json:"volume_uuid"`
	TotalBytes int64  `json:"total_bytes"`
	FreeBytes  int64  `json:"free_bytes"`
	Class      string `json:"class"`
	Verdict    string `json:"verdict"`
}

func renderStatusJSON(out io.Writer, d statusData) error {
	var j statusJSON
	j.Internal.MountPoint = d.Internal.MountPoint
	j.Internal.TotalBytes = d.Internal.TotalBytes
	j.Internal.FreeBytes = d.Internal.FreeBytes
	j.Internal.FitOK = d.Internal.Fit.OK
	j.Internal.BytesPerDay = d.Internal.Fit.BytesPerDay
	j.Internal.WindowDays = d.Internal.Fit.WindowDays
	j.Internal.HistoryDays = d.Internal.HistoryDays
	j.Internal.DaysUntilFull = d.Internal.DaysUntilFull
	j.Internal.FullOK = d.Internal.FullOK
	for _, v := range d.Externals {
		j.Externals = append(j.Externals, statusVolumeJSON{
			MountPoint: v.MountPoint, UUID: v.UUID,
			TotalBytes: v.TotalBytes, FreeBytes: v.FreeBytes,
			Class: string(v.Class), Verdict: volumeVerdict(v),
		})
	}
	j.Offload.Configured = d.Offload.Configured
	j.Offload.DestRoot = d.Offload.DestRoot
	j.Offload.Usable = d.Offload.Usable
	j.Offload.Detail = d.Offload.Detail
	adviceByKey := map[string]vmdisk.Advice{}
	for _, a := range d.VM.Advices {
		adviceByKey[a.VM.Kind+"/"+a.VM.Name] = a
	}
	for _, vm := range d.VM.VMs {
		row := statusVMJSON{
			Kind: vm.Kind, Name: vm.Name, Running: vm.Running,
			DatadiskPresent: vm.Datadisk.Present,
			TotalBytes:      vm.Datadisk.TotalBytes,
			UsedBytes:       vm.Datadisk.UsedBytes,
			PctFull:         vm.Datadisk.Pct(),
		}
		if a, ok := adviceByKey[vm.Kind+"/"+vm.Name]; ok {
			row.AdviceLevel = string(a.Level)
			row.Advice = a.Headline
			row.Command = a.Command
		}
		j.VMs = append(j.VMs, row)
	}
	j.Pressure.Batches24h = d.Pressure.Batches24h
	j.Pressure.NearContinuous = d.Pressure.NearContinuous
	if d.MemoryOK {
		m := d.Memory
		mj := &statusMemoryJSON{
			PhysicalBytes: m.PhysicalBytes, SwapUsedBytes: m.SwapUsedBytes, SwapTotalBytes: m.SwapTotalBytes,
			CompressorBytes: m.CompressorBytes, CompressorShare: m.CompressorShare(),
			Verdict: string(m.Verdict()), VerdictLine: m.VerdictLine(), Notes: m.Notes,
		}
		for _, p := range m.TopCompressed {
			mj.TopCompressed = append(mj.TopCompressed, statusMemProcJSON{
				PID: p.PID, Command: p.Command, ResidentBytes: p.ResidentBytes, CompressedBytes: p.CompressedBytes,
			})
		}
		j.Memory = mj
	}
	j.Note = appendNote(d.StoreNote, d.MemoryNote)
	j.Verdict = verdictSentence(d.Internal.TotalBytes, d.Externals, d.Offload)
	enc := json.NewEncoder(out)
	return enc.Encode(j)
}
