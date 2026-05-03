// Package ipc defines the JSON-RPC wire protocol between the noo-noo CLI and
// the noo-nood daemon, plus the small server/client wrappers around stdlib
// net/rpc/jsonrpc.
package ipc

import (
	"os"
	"path/filepath"
	"time"

	"github.com/FRIKKern/noo-noo/internal/heuristics"
	"github.com/FRIKKern/noo-noo/internal/store"
)

// SuggestionAlias re-exports heuristics.Suggestion so RPC reply types stay in
// the ipc package while reusing the canonical struct.
type SuggestionAlias = heuristics.Suggestion

// SocketEnv returns the default socket path, honoring the NOO_NOO_SOCK env var
// for tests.
func SocketEnv() string {
	if p := os.Getenv("NOO_NOO_SOCK"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "noo-noo", "noo-noo.sock")
}

// ----- Report -----

// ReportRequest is the (currently empty) argument to Report.Full.
type ReportRequest struct{}

// Report is the daemon's snapshot of recent activity, open suggestions, and
// repo idleness scores.
type Report struct {
	GeneratedAt     time.Time
	RecentActions   []store.StoredAction
	OpenSuggestions []SuggestionAlias
	RepoIdleness    []store.RepoIdleness
}

// ----- Suggestions -----

// SuggestionsRequest is the (currently empty) argument to Suggestions.List.
type SuggestionsRequest struct{}

// SuggestionsResponse is the reply for Suggestions.List.
type SuggestionsResponse struct {
	GeneratedAt time.Time
	Items       []SuggestionAlias
}

// DismissRequest names a single suggestion to dismiss.
type DismissRequest struct{ ID int64 }

// DismissResponse acknowledges a dismiss call.
type DismissResponse struct{ OK bool }

// ----- Clean -----

// CleanTarget is one filesystem path the daemon may free, with its measured
// size on disk.
type CleanTarget struct {
	Path  string
	Bytes int64
}

// CleanRequest asks the daemon to remove a list of targets, optionally as a
// dry run.
type CleanRequest struct {
	Targets []CleanTarget
	DryRun  bool
}

// CleanResponse summarises the bytes freed.
type CleanResponse struct {
	Targets    int
	BytesFreed int64
}

// ----- Daemon -----

// StatusRequest is the (currently empty) argument to Daemon.Status.
type StatusRequest struct{}

// StatusResponse describes the running daemon.
type StatusResponse struct {
	Running bool
	Version string
	Uptime  time.Duration
}
