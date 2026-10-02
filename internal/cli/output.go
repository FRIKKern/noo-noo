package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/modules"
)

// defaultTermWidth is the table width when stdout is not a terminal or the
// terminal cannot report its size.
const defaultTermWidth = 120

// sizeColWidth fits core.Bytes' widest rendering ("1023.9 GB").
const sizeColWidth = 9

// materialRatio is how far apparent and reclaimable must diverge before the
// apparent size earns a column: 20%.
const materialRatio = 1.2

// sizing is the display view of one Item's two sizes. Reclaimable is what
// deleting the item can actually free (the sizer's unique-allocated
// measurement when the module took one, Item.Size otherwise); Apparent is
// what du would say (allocated blocks when measured, Item.Size otherwise).
type sizing struct {
	Reclaimable core.Bytes
	Apparent    core.Bytes
	Measured    bool
}

// itemSizing derives the display sizes from an Item's evidence. Modules
// that truth-size (leaks, offload) stamp unique_allocated_bytes plus a
// du-style figure (blocks_bytes / allocated_bytes); everything else carries
// one number and is reported as-is, unmeasured.
func itemSizing(it modules.Item) sizing {
	s := sizing{Reclaimable: it.Size, Apparent: it.Size}
	if v, ok := evidenceBytes(it.Evidence, "unique_allocated_bytes"); ok {
		s.Reclaimable = v
		s.Measured = true
		for _, k := range []string{"blocks_bytes", "allocated_bytes"} {
			if a, ok := evidenceBytes(it.Evidence, k); ok {
				s.Apparent = a
				break
			}
		}
		if s.Apparent < s.Reclaimable {
			s.Apparent = s.Reclaimable
		}
	}
	return s
}

func evidenceBytes(ev map[string]string, key string) (core.Bytes, bool) {
	v, err := strconv.ParseInt(ev[key], 10, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return core.Bytes(v), true
}

// materiallyDifferent reports whether two sizes diverge by more than
// materialRatio in either direction.
func materiallyDifferent(a, b core.Bytes) bool {
	if a == b {
		return false
	}
	lo, hi := a, b
	if lo > hi {
		lo, hi = hi, lo
	}
	if lo <= 0 {
		return hi > 0
	}
	return float64(hi) > float64(lo)*materialRatio
}

// reportTotals returns the module's headline reclaimable bytes and the
// du-style apparent figure for the same set.
//
// Reclaimable is Report.Total, which the modules now compute from truth
// (leaks: the union of unique extents across hits; offload: unique bytes of
// relocate-class rows). Apparent re-derives the equivalent du figure from
// the rows: measured rows contribute their apparent size, and whatever part
// of Total the measured rows do not account for (an unmeasured module's
// whole total) is carried over unchanged. For a union total the measured
// rows over-account for it, so nothing carries over — exactly right.
func reportTotals(r modules.Report) (reclaimable, apparent core.Bytes) {
	var measuredReclaim, measuredApparent core.Bytes
	for _, it := range r.Items {
		s := itemSizing(it)
		if s.Measured {
			measuredReclaim += s.Reclaimable
			measuredApparent += s.Apparent
		}
	}
	rest := r.Total - measuredReclaim
	if rest < 0 {
		rest = 0
	}
	return r.Total, measuredApparent + rest
}

// itemRow is the NDJSON shape of one Item: the existing fields (Path, Size,
// Evidence) untouched, plus explicit reclaimable/apparent bytes so machine
// readers never have to guess which number Size is.
type itemRow struct {
	modules.Item
	ReclaimableBytes    int64 `json:"reclaimable_bytes"`
	ApparentBytes       int64 `json:"apparent_bytes"`
	ReclaimableMeasured bool  `json:"reclaimable_measured"`
}

// PrintReport writes a Report in either human-table or NDJSON form. The
// headline and the SIZE column are reclaimable bytes; the apparent (du)
// size appears only where it differs materially.
func PrintReport(out io.Writer, r modules.Report, asJSON bool) error {
	reclaimable, apparent := reportTotals(r)
	if asJSON {
		enc := json.NewEncoder(out)
		for _, it := range r.Items {
			s := itemSizing(it)
			row := itemRow{
				Item:                it,
				ReclaimableBytes:    int64(s.Reclaimable),
				ApparentBytes:       int64(s.Apparent),
				ReclaimableMeasured: s.Measured,
			}
			if err := enc.Encode(row); err != nil {
				return err
			}
		}
		return enc.Encode(map[string]any{
			"module":            r.Module,
			"total":             int64(r.Total),
			"reclaimable_bytes": int64(reclaimable),
			"apparent_bytes":    int64(apparent),
		})
	}

	_, _ = fmt.Fprintf(out, "%s module — %d item(s), %s reclaimable%s\n",
		r.Module, len(r.Items), reclaimable, apparentSuffix(reclaimable, apparent))

	sizes := make([]sizing, len(r.Items))
	showApparent := false
	var rowSum core.Bytes
	for i, it := range r.Items {
		sizes[i] = itemSizing(it)
		if sizes[i].Measured {
			rowSum += sizes[i].Reclaimable
			if materiallyDifferent(sizes[i].Reclaimable, sizes[i].Apparent) {
				showApparent = true
			}
		}
	}
	// Rows are each hit's OWN unique bytes; when they sum well past the
	// union total the hits are clones of one another, and the reader
	// deserves to be told why the rows do not add up.
	if rowSum > reclaimable && materiallyDifferent(rowSum, reclaimable) {
		_, _ = fmt.Fprintf(out, "(rows share APFS extents with each other: they sum to %s, but deleting all of them frees at most %s)\n",
			rowSum, reclaimable)
	}
	width := termWidth(out)
	pathWidth := width - sizeColWidth - 2
	if showApparent {
		pathWidth -= sizeColWidth + 2
		_, _ = fmt.Fprintf(out, "%*s  %*s  PATH\n", sizeColWidth, "SIZE", sizeColWidth, "APPARENT")
	} else {
		_, _ = fmt.Fprintf(out, "%*s  PATH\n", sizeColWidth, "SIZE")
	}
	for i, it := range r.Items {
		s := sizes[i]
		path := middleTruncate(it.Path, pathWidth)
		if !showApparent {
			_, _ = fmt.Fprintf(out, "%*s  %s\n", sizeColWidth, s.Reclaimable, path)
			continue
		}
		app := ""
		if s.Measured && materiallyDifferent(s.Reclaimable, s.Apparent) {
			app = s.Apparent.String()
		}
		_, _ = fmt.Fprintf(out, "%*s  %*s  %s\n", sizeColWidth, s.Reclaimable, sizeColWidth, app, path)
	}
	return nil
}

// apparentSuffix renders " (apparent X)" when the du-style figure differs
// materially from the reclaimable one, else nothing.
func apparentSuffix(reclaimable, apparent core.Bytes) string {
	if !materiallyDifferent(reclaimable, apparent) {
		return ""
	}
	return fmt.Sprintf(" (apparent %s)", apparent)
}

// termWidth returns the column count of out when it is a terminal, else
// defaultTermWidth. Width is clamped so a tiny terminal still gets a path.
func termWidth(out io.Writer) int {
	f, ok := out.(*os.File)
	if !ok {
		return defaultTermWidth
	}
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 {
		return defaultTermWidth
	}
	if ws.Col < 40 {
		return 40
	}
	return int(ws.Col)
}

// middleTruncate shortens s to at most max runes by replacing its middle
// with "…", keeping roughly a third of the head and the whole tail it can —
// the tail is where a path's identity lives.
func middleTruncate(s string, width int) string {
	n := utf8.RuneCountInString(s)
	if n <= width || width < 5 {
		return s
	}
	runes := []rune(s)
	head := (width - 1) / 3
	tail := width - 1 - head
	return string(runes[:head]) + "…" + string(runes[n-tail:])
}
