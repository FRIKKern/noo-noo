package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/rpc"
	"net/rpc/jsonrpc"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/FRIKKern/noo-noo/internal/store"
)

// Handlers bundles the four service objects registered on the RPC server. Any
// field left nil simply omits its namespace.
type Handlers struct {
	Report      *ReportService
	Suggestions *SuggestionsService
	Clean       *CleanService
	Daemon      *DaemonService
}

// ReportService is the receiver registered as "Report" on the RPC server.
// The Full method (in report_method.go) reads from Store to assemble the
// snapshot returned to `noo-noo report`.
type ReportService struct {
	Store *store.Store
}

// SuggestionsService is the receiver registered as "Suggestions". The List
// and Dismiss methods (in suggestions_method.go) read and update the
// suggestions table via Store.
type SuggestionsService struct {
	Store *store.Store
}

// CleanService is the receiver registered as "Clean". The Execute method
// (in clean_method.go) records that the user accepted a cleanup suggestion
// and returns a summary; the daemon does not perform deletes itself in
// Phase 0.2 (Phase 0.5 will introduce auto-clean).
type CleanService struct {
	Store *store.Store
	// Now is injectable for deterministic audit timestamps in tests. If nil,
	// Execute falls back to time.Now.
	Now func() time.Time
}

// DaemonService is the receiver registered as "Daemon". The Status method is
// defined here so the IPC foundation is end-to-end exercisable; task 39 may
// extend it with config-aware fields.
type DaemonService struct {
	StartedAt func() time.Time
	Version   string
}

// Status returns daemon liveness information. Implements Daemon.Status.
func (d *DaemonService) Status(_ StatusRequest, reply *StatusResponse) error {
	reply.Running = true
	reply.Version = d.Version
	if d.StartedAt != nil {
		reply.Uptime = time.Since(d.StartedAt())
	}
	return nil
}

// Server listens on a Unix socket and dispatches JSON-RPC requests.
type Server struct {
	socketPath string
	handlers   Handlers
	listener   net.Listener
	rpcSrv     *rpc.Server
	mu         sync.Mutex
	closed     bool
}

// NewServer constructs a server bound to socketPath. Start opens the socket.
func NewServer(socketPath string, h Handlers) *Server {
	return &Server{socketPath: socketPath, handlers: h}
}

// Start opens the listener and serves in a goroutine until ctx is canceled.
func (s *Server) Start(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.socketPath), 0o700); err != nil {
		return fmt.Errorf("mkdir socket dir: %w", err)
	}
	// Remove any stale socket file.
	_ = os.Remove(s.socketPath)
	l, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.socketPath, err)
	}
	s.listener = l

	srv := rpc.NewServer()
	if s.handlers.Report != nil {
		_ = srv.RegisterName("Report", s.handlers.Report)
	}
	if s.handlers.Suggestions != nil {
		_ = srv.RegisterName("Suggestions", s.handlers.Suggestions)
	}
	if s.handlers.Clean != nil {
		_ = srv.RegisterName("Clean", s.handlers.Clean)
	}
	if s.handlers.Daemon != nil {
		_ = srv.RegisterName("Daemon", s.handlers.Daemon)
	}
	s.rpcSrv = srv

	go s.acceptLoop()
	go func() {
		<-ctx.Done()
		s.Stop()
	}()
	return nil
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		go s.rpcSrv.ServeCodec(jsonrpc.NewServerCodec(conn))
	}
}

// Stop closes the listener and removes the socket file. Idempotent.
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.listener != nil {
		_ = s.listener.Close()
	}
	_ = os.Remove(s.socketPath)
}
