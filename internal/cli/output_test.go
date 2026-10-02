package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/modules"
)

const (
	testGB = core.Bytes(1 << 30)
	testMB = core.Bytes(1 << 20)
)

// cloneSiblings is the founding incident in two rows: each hit's own unique
// bytes are 1 GB (du says 2 GB each), and the module's union total is 1 GB
// because the two are clones of one another.
func cloneSiblings() modules.Report {
	mk := func(path string) modules.Item {
		return modules.Item{
			Path: path,
			Size: testGB,
			Evidence: map[string]string{
				"unique_allocated_bytes": "1073741824",
				"blocks_bytes":           "2147483648",
				"staleness":              "stale",
			},
		}
	}
	return modules.Report{
		Module: "leaks",
		Items: []modules.Item{
			mk("/Users/me/Library/Application Support/Google/Chrome/code_sign_clone/a"),
			mk("/Users/me/Library/Application Support/Google/Chrome/code_sign_clone/" + strings.Repeat("deep/", 30) + "tail.app"),
		},
		Total: testGB,
	}
}

func TestPrintReportHeadlineIsReclaimable(t *testing.T) {
	var buf bytes.Buffer
	if err := PrintReport(&buf, cloneSiblings(), false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if want := "leaks module — 2 item(s), 1.0 GB reclaimable (apparent 4.0 GB)"; lines[0] != want {
		t.Errorf("headline = %q, want %q", lines[0], want)
	}
	if want := "(rows share APFS extents with each other: they sum to 2.0 GB, but deleting all of them frees at most 1.0 GB)"; lines[1] != want {
		t.Errorf("clone note = %q, want %q", lines[1], want)
	}
	if !strings.Contains(lines[2], "SIZE") || !strings.Contains(lines[2], "APPARENT") || !strings.HasSuffix(lines[2], "PATH") {
		t.Errorf("header = %q, want SIZE / APPARENT / PATH", lines[2])
	}
	// Rows: fixed-width right-aligned size first, then apparent, then path.
	if !strings.HasPrefix(lines[3], "   1.0 GB     2.0 GB  /Users/me/") {
		t.Errorf("row = %q, want right-aligned 1.0 GB / 2.0 GB then path", lines[3])
	}
	for i, l := range lines {
		if n := utf8.RuneCountInString(l); n > defaultTermWidth {
			t.Errorf("line %d is %d runes wide, want <= %d: %q", i, n, defaultTermWidth, l)
		}
		if strings.Contains(l, "    /") && strings.HasSuffix(l, "  ") {
			t.Errorf("line %d has trailing padding: %q", i, l)
		}
	}
	// Long path: middle-truncated, tail kept.
	if !strings.Contains(lines[4], "…") || !strings.HasSuffix(lines[4], "tail.app") {
		t.Errorf("long path row = %q, want middle ellipsis and the tail kept", lines[4])
	}
}

func TestPrintReportUnmeasuredModuleHasNoApparentColumn(t *testing.T) {
	rep := modules.Report{
		Module: "dev",
		Items: []modules.Item{
			{Path: "/repo/node_modules", Size: 300 * testMB, Evidence: map[string]string{"size_bytes": "314572800"}},
		},
		Total: 300 * testMB,
	}
	var buf bytes.Buffer
	if err := PrintReport(&buf, rep, false); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if want := "dev module — 1 item(s), 300.0 MB reclaimable"; lines[0] != want {
		t.Errorf("headline = %q, want %q", lines[0], want)
	}
	if want := "     SIZE  PATH"; lines[1] != want {
		t.Errorf("header = %q, want %q", lines[1], want)
	}
	if want := " 300.0 MB  /repo/node_modules"; lines[2] != want {
		t.Errorf("row = %q, want %q", lines[2], want)
	}
}

func TestPrintReportJSONCarriesExplicitReclaimable(t *testing.T) {
	var buf bytes.Buffer
	if err := PrintReport(&buf, cloneSiblings(), true); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 2 rows + 1 total line, got %d: %s", len(lines), buf.String())
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"Path", "Size", "Evidence"} {
		if _, ok := row[k]; !ok {
			t.Errorf("existing field %q dropped from row: %v", k, row)
		}
	}
	if row["reclaimable_bytes"] != float64(testGB) || row["apparent_bytes"] != float64(2*testGB) || row["reclaimable_measured"] != true {
		t.Errorf("row sizes wrong: %v", row)
	}
	var total map[string]any
	if err := json.Unmarshal([]byte(lines[2]), &total); err != nil {
		t.Fatal(err)
	}
	if total["module"] != "leaks" || total["total"] != float64(testGB) ||
		total["reclaimable_bytes"] != float64(testGB) || total["apparent_bytes"] != float64(4*testGB) {
		t.Errorf("total line wrong: %v", total)
	}
}

// TestReportTotalsOffloadShape: only measured (relocate) rows re-derive an
// apparent figure; an informational native-config row that is not part of
// Total must not leak into it either.
func TestReportTotalsOffloadShape(t *testing.T) {
	rep := modules.Report{
		Module: "offload",
		Items: []modules.Item{
			{Path: "/Users/me/.docker", Size: 5 * testGB, Evidence: map[string]string{
				"allocated_bytes": "5368709120", "unique_allocated_bytes": "3221225472"}},
			{Path: "/Users/me/Library/pnpm", Size: 10 * testGB, Evidence: map[string]string{
				"allocated_bytes": "10737418240", "class": "native-config"}},
		},
		Total: 3 * testGB,
	}
	reclaim, apparent := reportTotals(rep)
	if reclaim != 3*testGB || apparent != 5*testGB {
		t.Errorf("reportTotals = (%s, %s), want (3.0 GB, 5.0 GB)", reclaim, apparent)
	}
}

func TestMiddleTruncate(t *testing.T) {
	cases := []struct{ in, want string }{
		{"short", "short"},
		{"/a/b/c/d/e/f/g/h/i/j/k/l/m/n/o/p", "/a/b/…/l/m/n/o/p"},
	}
	for _, c := range cases {
		if got := middleTruncate(c.in, 16); got != c.want {
			t.Errorf("middleTruncate(%q, 16) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := middleTruncate("ünïcödé-path-with-a-tail", 10); utf8.RuneCountInString(got) != 10 || !strings.HasSuffix(got, "a-tail") {
		t.Errorf("rune-aware truncate wrong: %q", got)
	}
}

func TestMateriallyDifferent(t *testing.T) {
	if materiallyDifferent(100, 110) {
		t.Error("10% apart is not material")
	}
	if !materiallyDifferent(100, 130) || !materiallyDifferent(130, 100) {
		t.Error("30% apart is material, either direction")
	}
	if materiallyDifferent(0, 0) || !materiallyDifferent(0, 1) {
		t.Error("zero handling wrong")
	}
}
