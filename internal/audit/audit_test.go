package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuditWrite(t *testing.T) {
	dir := t.TempDir()
	log, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer log.Close()

	rec := Record{
		Timestamp: time.Date(2026, 5, 2, 14, 30, 0, 0, time.UTC),
		Module:    "dev",
		Op:        "delete",
		Target:    "/Users/foo/repo/node_modules",
		Size:      2048,
		Evidence:  map[string]string{"last_commit_days": "127"},
		Outcome:   "ok",
	}
	if err := log.Write(rec); err != nil {
		t.Fatalf("Write: %v", err)
	}

	files, err := filepath.Glob(filepath.Join(dir, "audit-*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("expected one audit file, got %v (err %v)", files, err)
	}
	f, err := os.Open(files[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		t.Fatal("expected one line")
	}
	var got Record
	if err := json.Unmarshal(scanner.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v (line: %s)", err, scanner.Text())
	}
	if got.Module != "dev" || got.Op != "delete" || got.Size != 2048 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if !strings.Contains(scanner.Text(), `"evidence":{"last_commit_days":"127"}`) {
		t.Errorf("evidence not serialized as expected: %s", scanner.Text())
	}
}

func TestAuditAppend(t *testing.T) {
	dir := t.TempDir()
	log, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := log.Write(Record{Module: "x", Op: "y", Outcome: "ok"}); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	log.Close()

	files, _ := filepath.Glob(filepath.Join(dir, "audit-*.jsonl"))
	data, _ := os.ReadFile(files[0])
	lines := strings.Count(string(data), "\n")
	if lines != 3 {
		t.Errorf("expected 3 lines, got %d", lines)
	}
}
