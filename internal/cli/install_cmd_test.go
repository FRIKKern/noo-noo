package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FRIKKern/noo-noo/internal/launchd"
)

type captureRunner struct{ calls []string }

func (c *captureRunner) Run(name string, args ...string) ([]byte, error) {
	c.calls = append(c.calls, name+" "+strings.Join(args, " "))
	return nil, nil
}

func TestInstallCmdWritesPlistAndCallsLaunchctl(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(t.TempDir(), "Library", "Logs", "noo-noo")
	r := &captureRunner{}
	out := &bytes.Buffer{}
	cmd := newInstallCmd(installOpts{
		LaunchAgentDir: dir,
		LogDir:         logDir,
		ProgramPath:    "/usr/local/bin/noo-nood",
		Runner:         launchd.NewWithRunner(r),
		Out:            out,
	})
	if err := cmd.Run([]string{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	plistPath := filepath.Join(dir, "io.noo-noo.d.plist")
	data, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatalf("plist not written: %v", err)
	}
	if !bytes.Contains(data, []byte("io.noo-noo.d")) {
		t.Errorf("plist missing label, got: %s", data)
	}
	if len(r.calls) == 0 || !strings.Contains(r.calls[0], "bootstrap") {
		t.Errorf("expected launchctl bootstrap, got %v", r.calls)
	}
	// Daemon logs live under the log dir, which install creates up front.
	for _, want := range []string{
		filepath.Join(logDir, "noo-nood.log"),
		filepath.Join(logDir, "noo-nood.err.log"),
	} {
		if !bytes.Contains(data, []byte("<string>"+want+"</string>")) {
			t.Errorf("plist missing log path %q:\n%s", want, data)
		}
	}
	if bytes.Contains(data, []byte("/tmp/noo-nood")) {
		t.Errorf("plist still logs to /tmp:\n%s", data)
	}
	if st, err := os.Stat(logDir); err != nil || !st.IsDir() {
		t.Errorf("log dir not created: %v", err)
	}
	if !strings.Contains(out.String(), logDir) {
		t.Errorf("install output should name the log dir, got %q", out.String())
	}
}

func TestUninstallCmdRemovesPlistAndCallsLaunchctl(t *testing.T) {
	dir := t.TempDir()
	plistPath := filepath.Join(dir, "io.noo-noo.d.plist")
	if err := os.WriteFile(plistPath, []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &captureRunner{}
	out := &bytes.Buffer{}
	cmd := newUninstallCmd(installOpts{
		LaunchAgentDir: dir,
		Runner:         launchd.NewWithRunner(r),
		Out:            out,
	})
	if err := cmd.Run([]string{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Errorf("plist still present after uninstall")
	}
	if len(r.calls) == 0 || !strings.Contains(r.calls[0], "bootout") {
		t.Errorf("expected launchctl bootout, got %v", r.calls)
	}
}
