package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/FRIKKern/noo-noo/internal/cli"
)

const version = "0.5.0"

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		_, _ = fmt.Fprintf(os.Stdout, "noo-noo %s\n", version)
		os.Exit(0)
	}

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	app := &cli.App{Out: os.Stdout, Err: os.Stderr}
	os.Exit(app.Run(ctx, os.Args))
}
