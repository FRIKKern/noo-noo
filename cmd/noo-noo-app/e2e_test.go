package main

import (
	"context"
	"net"
	"net/rpc"
	"net/rpc/jsonrpc"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/ipc"
	"github.com/FRIKKern/noo-noo/internal/menubar"
)

// mkShortTempDir creates a temp dir under /tmp so the resulting Unix
// socket path stays under macOS's 104-byte sun_path limit.
func mkShortTempDir(t *testing.T) (string, error) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "nn-e2e-")
	if err != nil {
		return "", err
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir, nil
}

// fakeDaemonSvc registers as RPC service "Daemon" and counts calls. It's a
// drop-in stand-in for the real noo-nood daemon used by these e2e tests so
// we exercise the full IPC wire format end-to-end without booting SQLite or
// the scheduler.
type fakeDaemonSvc struct {
	statusCalls  int64
	triggerCalls int64
}

func (f *fakeDaemonSvc) Status(_ ipc.StatusRequest, reply *ipc.StatusResponse) error {
	atomic.AddInt64(&f.statusCalls, 1)
	*reply = ipc.StatusResponse{Running: true, Version: "test", Uptime: time.Second}
	return nil
}

func (f *fakeDaemonSvc) TriggerScan(_ ipc.TriggerScanArgs, reply *ipc.TriggerScanReply) error {
	atomic.AddInt64(&f.triggerCalls, 1)
	*reply = ipc.TriggerScanReply{Ok: true, SuggestionsAdded: 1, DurationMs: 5}
	return nil
}

// fakeSuggestionsSvc stands in for the real Suggestions service so the
// menubar shim's status path (DaemonStatus + SuggestionsList) populates a
// non-zero OpenSuggestions count.
type fakeSuggestionsSvc struct {
	openSugg int
}

func (f *fakeSuggestionsSvc) List(_ ipc.SuggestionsRequest, resp *ipc.SuggestionsResponse) error {
	resp.GeneratedAt = time.Now()
	resp.Items = make([]ipc.SuggestionAlias, f.openSugg)
	return nil
}

// startFakeDaemon binds a Unix socket and serves Daemon + Suggestions RPCs
// via JSON-RPC. The socket path is returned; cleanup happens via t.Cleanup.
// We use os.MkdirTemp under /tmp (rather than t.TempDir) because macOS caps
// AF_UNIX paths at 104 bytes and t.TempDir paths can blow past that.
func startFakeDaemon(t *testing.T, daemon *fakeDaemonSvc, sugg *fakeSuggestionsSvc) string {
	t.Helper()
	dir, err := mkShortTempDir(t)
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "n.sock")
	srv := rpc.NewServer()
	if err := srv.RegisterName("Daemon", daemon); err != nil {
		t.Fatal(err)
	}
	if sugg != nil {
		if err := srv.RegisterName("Suggestions", sugg); err != nil {
			t.Fatal(err)
		}
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.ServeCodec(jsonrpc.NewServerCodec(c))
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return sock
}

// TestEndToEnd_PollerToTray drives the full path: fake daemon -> ipc.Client
// -> ipcClientShim -> menubar.Poller -> refreshTray -> fakeTray. Asserts
// the badge text reflects OpenSuggestions reported by the fake daemon.
func TestEndToEnd_PollerToTray(t *testing.T) {
	daemon := &fakeDaemonSvc{}
	sugg := &fakeSuggestionsSvc{openSugg: 2}
	sock := startFakeDaemon(t, daemon, sugg)

	cli, err := ipc.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cli.Close() }()

	tray := &fakeTray{}
	var mu sync.Mutex
	poll := menubar.NewPoller(&ipcClientShim{c: cli}, 10*time.Millisecond,
		func(s menubar.Status) {
			mu.Lock()
			defer mu.Unlock()
			refreshTray(tray, s)
		})
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		poll.Run(ctx)
		close(done)
	}()
	<-done

	mu.Lock()
	gotTitle := tray.title
	mu.Unlock()
	if gotTitle != "2" {
		t.Errorf("tray.title = %q, want %q", gotTitle, "2")
	}
	if got := atomic.LoadInt64(&daemon.statusCalls); got < 3 {
		t.Errorf("statusCalls = %d, want >=3", got)
	}
}

// TestEndToEnd_TriggerScanReachesDaemon verifies a Daemon.TriggerScan call
// over the wire reaches the fake daemon. ipc.Client doesn't yet expose a
// typed TriggerScan helper (Phase 0.3.1), so we issue the call via a raw
// JSON-RPC client over the same socket — same wire format the menubar
// "Run Scan Now" path will use once the helper lands.
func TestEndToEnd_TriggerScanReachesDaemon(t *testing.T) {
	daemon := &fakeDaemonSvc{}
	sock := startFakeDaemon(t, daemon, nil)

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	rcli := jsonrpc.NewClient(conn)
	defer func() { _ = rcli.Close() }()

	var reply ipc.TriggerScanReply
	if err := rcli.Call("Daemon.TriggerScan", ipc.TriggerScanArgs{}, &reply); err != nil {
		t.Fatalf("TriggerScan: %v", err)
	}
	if !reply.Ok {
		t.Errorf("reply.Ok = false, want true")
	}
	if got := atomic.LoadInt64(&daemon.triggerCalls); got != 1 {
		t.Errorf("triggerCalls = %d, want 1", got)
	}
}
