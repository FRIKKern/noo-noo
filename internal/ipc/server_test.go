package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubHandlers returns a Handlers with only Daemon wired (enough for the
// roundtrip test).
func stubHandlers() Handlers {
	return Handlers{
		Daemon: &DaemonService{
			StartedAt: func() time.Time { return time.Now().Add(-time.Minute) },
			Version:   "test",
		},
	}
}

func TestServerRoundtrip(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "noo.sock")
	srv := NewServer(sock, stubHandlers())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	c, err := Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	resp, err := c.DaemonStatus()
	if err != nil {
		t.Fatalf("DaemonStatus: %v", err)
	}
	if resp.Version != "test" {
		t.Errorf("Version = %q, want test", resp.Version)
	}
}

// TestServerSecondInstanceRefuses: a second Server on the same socket path
// must fail Start with ErrAlreadyRunning, name the holder's pid, and leave
// the first daemon's socket answering. Its Stop must not unlink the socket.
func TestServerSecondInstanceRefuses(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "noo.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := NewServer(sock, stubHandlers())
	if err := first.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	defer first.Stop()

	second := NewServer(sock, stubHandlers())
	err := second.Start(ctx)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Start err = %v, want ErrAlreadyRunning", err)
	}
	wantPID := fmt.Sprintf("pid %d", os.Getpid())
	if !strings.Contains(err.Error(), wantPID) || !strings.Contains(err.Error(), "noo-noo daemon status") {
		t.Errorf("refusal message %q should name %q and the status command", err, wantPID)
	}

	// Stop of the refused (non-owner) instance must not unlink the socket.
	second.Stop()
	if _, err := os.Lstat(sock); err != nil {
		t.Fatalf("non-owner Stop removed the live socket: %v", err)
	}
	c, err := Dial(sock)
	if err != nil {
		t.Fatalf("first daemon unreachable after non-owner Stop: %v", err)
	}
	defer func() { _ = c.Close() }()
	resp, err := c.DaemonStatus()
	if err != nil {
		t.Fatalf("DaemonStatus: %v", err)
	}
	if resp.PID != os.Getpid() {
		t.Errorf("PID = %d, want %d", resp.PID, os.Getpid())
	}

	// Owner Stop releases both the socket and the lock; a new instance may
	// then start.
	first.Stop()
	if _, err := os.Lstat(sock); !os.IsNotExist(err) {
		t.Errorf("owner Stop left the socket behind: %v", err)
	}
	third := NewServer(sock, stubHandlers())
	if err := third.Start(ctx); err != nil {
		t.Fatalf("Start after owner Stop: %v", err)
	}
	third.Stop()
}

// TestServerStaleSocketReplaced: a socket file nobody answers on (daemon
// died without unlinking) is removed and re-bound.
func TestServerStaleSocketReplaced(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "noo.sock")
	// Fabricate a stale socket: listen, close WITHOUT unlinking.
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = l.Close()
	if _, err := os.Lstat(sock); err != nil {
		t.Fatalf("stale socket fixture missing: %v", err)
	}

	srv := NewServer(sock, stubHandlers())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start over stale socket: %v", err)
	}
	defer srv.Stop()
	c, err := Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	_ = c.Close()
}
