package launchd

import (
	"bytes"
	"os"
	"testing"
)

func TestPlistGoldenFile(t *testing.T) {
	got, err := GeneratePlist("io.noo-noo.d", "/usr/local/bin/noo-nood", nil, true, true)
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
		[]string{"--config", "/etc/noo-noo.toml"}, true, true)
	if err != nil {
		t.Fatalf("GeneratePlist: %v", err)
	}
	if !bytes.Contains(got, []byte("<string>--config</string>")) {
		t.Errorf("expected --config in output, got: %s", got)
	}
}
