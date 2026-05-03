package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/modules"
	"github.com/FRIKKern/noo-noo/internal/modules/caches"
	"github.com/FRIKKern/noo-noo/internal/modules/dev"
	"github.com/FRIKKern/noo-noo/internal/modules/startup"
)

func init() { Register("report", reportCmd) }

func reportCmd(ctx context.Context, app *App, args []string) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(app.Err)
	asJSON := fs.Bool("json", false, "output NDJSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	all := []modules.Module{
		dev.New([]string{filepath.Join(homeDir(), "Documents", "GitHub")},
			core.NewSafety([]string{filepath.Join(homeDir(), "Documents", "GitHub")}, []string{".git"})),
		caches.New(defaultCacheTargets(),
			core.NewSafety([]string{filepath.Join(homeDir(), "Library", "Caches")}, nil)),
		startup.New(defaultStartupConfig(), startup.ExecRunner{},
			filepath.Join(auditDir(), "startup-restore.jsonl"), os.Getuid()),
	}

	var grand core.Bytes
	for _, m := range all {
		rep, err := m.Scan(ctx)
		if err != nil {
			_, _ = fmt.Fprintf(app.Err, "%s scan failed: %v\n", m.Name(), err)
			continue
		}
		_ = PrintReport(app.Out, rep, *asJSON)
		grand += rep.Total
		_, _ = fmt.Fprintln(app.Out)
	}
	if !*asJSON {
		_, _ = fmt.Fprintf(app.Out, "Grand total reclaimable: %s\n", grand)
	}
	return 0
}
