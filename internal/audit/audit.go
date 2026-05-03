// Package audit writes an append-only JSONL log of every destructive action.
// Files are named audit-YYYY-MM-DD.jsonl and never rotated by noo-noo;
// rotation is left to newsyslog or user discretion.
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Record is one entry in the audit log.
type Record struct {
	Timestamp time.Time         `json:"ts"`
	Module    string            `json:"module"`
	Op        string            `json:"op"`
	Target    string            `json:"target,omitempty"`
	Size      int64             `json:"size,omitempty"`
	Evidence  map[string]string `json:"evidence,omitempty"`
	Outcome   string            `json:"outcome"`
	Error     string            `json:"error,omitempty"`
}

// Log is the append-only audit writer.
type Log struct {
	dir string
	mu  sync.Mutex
	f   *os.File
	day string // "2026-05-02"
}

// New opens (or creates) an audit log under dir. dir is created if missing.
func New(dir string) (*Log, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir audit dir: %w", err)
	}
	return &Log{dir: dir}, nil
}

// Write appends one record. Timestamp is filled if zero.
func (l *Log) Write(r Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now().UTC()
	}
	day := r.Timestamp.Format("2006-01-02")
	if err := l.rotateLocked(day); err != nil {
		return err
	}
	enc, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal record: %w", err)
	}
	if _, err := l.f.Write(append(enc, '\n')); err != nil {
		return fmt.Errorf("write audit: %w", err)
	}
	return nil
}

// Close releases the underlying file. Safe to call multiple times.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

func (l *Log) rotateLocked(day string) error {
	if l.f != nil && l.day == day {
		return nil
	}
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
	path := filepath.Join(l.dir, fmt.Sprintf("audit-%s.jsonl", day))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open audit file: %w", err)
	}
	l.f = f
	l.day = day
	return nil
}
