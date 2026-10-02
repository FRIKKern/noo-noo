package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/modules"
	"github.com/FRIKKern/noo-noo/internal/modules/dev"
	"github.com/FRIKKern/noo-noo/internal/modules/startup"
)

func init() { Register("report", reportCmd) }

// memSourcesFn supplies the memory section's inputs; tests swap in fakes.
var memSourcesFn = defaultMemSources

func reportCmd(ctx context.Context, app *App, args []string) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(app.Err)
	asJSON := fs.Bool("json", false, "output NDJSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// Memory first: the usage text promises it, and it is the cheapest,
	// most immediate part of a diagnosis.
	mem := gatherMemory(ctx, memSourcesFn())
	if *asJSON {
		_ = renderMemoryJSON(app.Out, mem)
	} else {
		renderMemory(app.Out, mem)
		_, _ = fmt.Fprintln(app.Out)
	}

	_, offloadMod := offloadSetup()
	all := []modules.Module{
		dev.New([]string{filepath.Join(homeDir(), "Documents", "GitHub")},
			core.NewSafety([]string{filepath.Join(homeDir(), "Documents", "GitHub")}, []string{".git"})),
		newCachesModule(),
		newLeaksModule(),
		newAppsModule(),
		startup.New(defaultStartupConfig(), startup.ExecRunner{},
			filepath.Join(auditDir(), "startup-restore.jsonl"), os.Getuid()),
		offloadMod,
	}

	var grand, grandApparent core.Bytes
	for _, m := range all {
		rep, err := m.Scan(ctx)
		if err != nil {
			_, _ = fmt.Fprintf(app.Err, "%s scan failed: %v\n", m.Name(), err)
			continue
		}
		_ = PrintReport(app.Out, rep, *asJSON)
		reclaimable, apparent := reportTotals(rep)
		grand += reclaimable
		grandApparent += apparent
		_, _ = fmt.Fprintln(app.Out)
	}
	if !*asJSON {
		_, _ = fmt.Fprintf(app.Out, "Grand total reclaimable: %s%s\n", grand, apparentSuffix(grand, grandApparent))
	}
	return 0
}
