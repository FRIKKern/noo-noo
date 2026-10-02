package launchd

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestPlistGoldenFile(t *testing.T) {
	got, err := GeneratePlist("io.noo-noo.d", "/usr/local/bin/noo-nood", nil, true, true,
		"/Users/test/Library/Logs/noo-noo")
	if err != nil {
		t.Fatalf("GeneratePlist: %v", err)
	}
	want, err := os.ReadFile("testdata/golden.plist")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("plist mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestPlistWithExtraArgs(t *testing.T) {
	got, err := GeneratePlist("io.noo-noo.d", "/usr/local/bin/noo-nood",
		[]string{"--config", "/etc/noo-noo.toml"}, true, true, "/tmp/logs")
	if err != nil {
		t.Fatalf("GeneratePlist: %v", err)
	}
	if !bytes.Contains(got, []byte("<string>--config</string>")) {
		t.Errorf("expected --config in output, got: %s", got)
	}
}

// TestPlistLogsUnderLibraryLogs pins the log location users (and the cask
// zap stanza) expect: ~/Library/Logs/noo-noo, never /tmp.
func TestPlistLogsUnderLibraryLogs(t *testing.T) {
	dir := DefaultLogDir()
	if !strings.HasSuffix(dir, "/Library/Logs/noo-noo") {
		t.Fatalf("DefaultLogDir = %q, want ~/Library/Logs/noo-noo", dir)
	}
	got, err := GeneratePlist("io.noo-noo.d", "/usr/local/bin/noo-nood", nil, true, true, dir)
	if err != nil {
		t.Fatalf("GeneratePlist: %v", err)
	}
	for _, want := range []string{
		"<string>" + dir + "/noo-nood.log</string>",
		"<string>" + dir + "/noo-nood.err.log</string>",
	} {
		if !bytes.Contains(got, []byte(want)) {
			t.Errorf("plist missing %q:\n%s", want, got)
		}
	}
	if bytes.Contains(got, []byte("/tmp/")) {
		t.Errorf("plist still logs under /tmp:\n%s", got)
	}
}
