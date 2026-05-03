package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/FRIKKern/noo-noo/internal/modules"
)

// PrintReport writes a Report in either human-table or NDJSON form.
func PrintReport(out io.Writer, r modules.Report, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(out)
		for _, it := range r.Items {
			if err := enc.Encode(it); err != nil {
				return err
			}
		}
		return enc.Encode(map[string]any{
			"module": r.Module,
			"total":  int64(r.Total),
		})
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', tabwriter.AlignRight)
	_, _ = fmt.Fprintf(w, "%s module — %d item(s), %s total\n", r.Module, len(r.Items), r.Total)
	_, _ = fmt.Fprintln(w, "SIZE\tPATH\t")
	for _, it := range r.Items {
		_, _ = fmt.Fprintf(w, "%s\t%s\t\n", it.Size, it.Path)
	}
	return w.Flush()
}
