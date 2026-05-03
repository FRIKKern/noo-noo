package ipc

import (
	"context"
	"path/filepath"
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
