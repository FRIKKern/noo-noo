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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/FRIKKern/noo-noo/internal/store"
)

// ErrAlreadyRunning is returned by Server.Start when another noo-nood owns
// the socket. Callers match it with errors.Is; the message carries the
// holder's pid when the lock file names one.
var ErrAlreadyRunning = errors.New("noo-nood already running")

// Handlers bundles the four service objects registered on the RPC server. Any
// field left nil simply omits its namespace.
type Handlers struct {
	Report      *ReportService
	Suggestions *SuggestionsService
	Clean       *CleanService
	Daemon      *DaemonService
	AutoClean   *AutoCleanService
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

// DaemonService is the receiver registered as "Daemon". Its Status method
// lives in daemon_method.go to mirror the per-namespace file split used by
// Report, Suggestions, and Clean.
type DaemonService struct {
	StartedAt func() time.Time
	Version   string
	// sched is the scheduler hook backing TriggerScan. May be nil in tests
	// or in early daemon boot before the scheduler is wired; TriggerScan
	// guards against the nil case so a stray call cannot panic.
	sched SchedulerKicker
}

// WithScheduler wires the TriggerScan backend and returns the service for
// literal-style chaining in main.go. Without this call every force-scan
// answers "no scheduler wired" — before the nil guard existed it was worse:
// the unset field panicked the daemon on the first Run Scan Now.
func (d *DaemonService) WithScheduler(k SchedulerKicker) *DaemonService {
	d.sched = k
	return d
}

// Server listens on a Unix socket and dispatches JSON-RPC requests.
//
// Single-instance law: exactly one noo-nood may own a socket path. Start
// takes an exclusive flock on <socket>.lock BEFORE touching the socket; a
// second daemon (a bare `noo-nood` in a terminal beside the launchd one)
// fails Start with ErrAlreadyRunning and never unlinks the live socket.
// Proven necessary: the unguarded version unlinked the launchd daemon's
// socket on start and again on its own stop, leaving the first daemon
// alive but unreachable until `launchctl kickstart -k`.
type Server struct {
	socketPath string
	handlers   Handlers
	listener   net.Listener
	rpcSrv     *rpc.Server
	mu         sync.Mutex
	closed     bool
	// lock is the held instance lock; nil when this Server never acquired
	// it (Start refused, or Start never ran).
	lock *os.File
	// bound is true only after THIS process listened on socketPath. Stop
	// unlinks the socket only when bound — a non-owner must never remove
	// another daemon's socket.
	bound bool
}

// NewServer constructs a server bound to socketPath. Start opens the socket.
func NewServer(socketPath string, h Handlers) *Server {
	return &Server{socketPath: socketPath, handlers: h}
}

// LockPath is the instance lock file guarding socketPath.
func LockPath(socketPath string) string { return socketPath + ".lock" }

// Start opens the listener and serves in a goroutine until ctx is canceled.
// Returns ErrAlreadyRunning (wrapped, with the holder's pid when known) if
// another daemon owns the socket; nothing on disk is touched in that case.
func (s *Server) Start(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.socketPath), 0o700); err != nil {
		return fmt.Errorf("mkdir socket dir: %w", err)
	}
	lock, err := acquireInstanceLock(LockPath(s.socketPath))
	if err != nil {
		return err
	}
	s.lock = lock

	// We hold the lock, so any socket file present is either stale (a
	// previous daemon died without unlinking) or owned by a daemon that
	// predates the lock. Only remove it when nobody answers on it.
	if socketAnswers(s.socketPath) {
		s.releaseLock()
		return fmt.Errorf("%w (socket %s answers; lock held by nobody — older daemon?); use `noo-noo daemon status`",
			ErrAlreadyRunning, s.socketPath)
	}
	_ = os.Remove(s.socketPath)

	l, err := net.Listen("unix", s.socketPath)
	if err != nil {
		s.releaseLock()
		return fmt.Errorf("listen %s: %w", s.socketPath, err)
	}
	s.listener = l
	s.bound = true

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
	if s.handlers.AutoClean != nil {
		_ = srv.RegisterName("AutoClean", s.handlers.AutoClean)
	}
	s.rpcSrv = srv

	go s.acceptLoop()
	go func() {
		<-ctx.Done()
		s.Stop()
	}()
	return nil
}

// acquireInstanceLock opens lockPath and takes an exclusive, non-blocking
// flock on it. On contention it reads the holder's pid out of the file and
// returns ErrAlreadyRunning naming it. The lock is released by the kernel
// when the holder exits, however it died — no stale-lock recovery needed.
func acquireInstanceLock(lockPath string) (*os.File, error) {
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open instance lock %s: %w", lockPath, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder := readLockPID(f)
		_ = f.Close()
		if holder > 0 {
			return nil, fmt.Errorf("%w (pid %d) — use `noo-noo daemon status`", ErrAlreadyRunning, holder)
		}
		return nil, fmt.Errorf("%w (lock %s held) — use `noo-noo daemon status`", ErrAlreadyRunning, lockPath)
	}
	// We own it: stamp our pid so the next contender can name us.
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return f, nil
}

// readLockPID parses the pid a lock holder stamped into the file; 0 if the
// file is empty or unparseable.
func readLockPID(f *os.File) int {
	buf := make([]byte, 32)
	n, _ := f.ReadAt(buf, 0)
	pid, err := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// socketAnswers reports whether something accepts connections on path. A
// missing file or ECONNREFUSED (file present, no listener) both mean "stale".
func socketAnswers(path string) bool {
	if _, err := os.Lstat(path); err != nil {
		return false
	}
	c, err := net.DialTimeout("unix", path, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// releaseLock drops the instance lock and its file. Callers hold s.mu or
// are still single-threaded inside Start.
func (s *Server) releaseLock() {
	if s.lock == nil {
		return
	}
	_ = os.Remove(s.lock.Name())
	_ = s.lock.Close() // closing releases the flock
	s.lock = nil
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

// Stop closes the listener and removes the socket file — but ONLY the
// socket this process bound. A Server whose Start was refused owns nothing
// on disk and leaves the live daemon's socket alone. Idempotent.
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
	if s.bound {
		_ = os.Remove(s.socketPath)
		s.bound = false
	}
	s.releaseLock()
}
