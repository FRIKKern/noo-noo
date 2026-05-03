package launchd

import (
	"strings"
	"testing"
)

type fakeRunner struct {
	calls   []string
	results map[string][]byte
	errs    map[string]error
}

func (f *fakeRunner) Run(name string, args ...string) ([]byte, error) {
	cmd := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, cmd)
	if f.errs != nil {
		if err, ok := f.errs[args[0]]; ok {
			return nil, err
		}
	}
	if f.results != nil {
		if r, ok := f.results[args[0]]; ok {
			return r, nil
		}
	}
	return nil, nil
}

func TestInstallCallsBootstrap(t *testing.T) {
	r := &fakeRunner{}
	m := NewWithRunner(r)
	if err := m.Install("/tmp/io.noo-noo.d.plist"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %v", len(r.calls), r.calls)
	}
	if !strings.Contains(r.calls[0], "bootstrap") || !strings.Contains(r.calls[0], "/tmp/io.noo-noo.d.plist") {
		t.Errorf("call wrong: %s", r.calls[0])
	}
	if !strings.Contains(r.calls[0], "gui/") {
		t.Errorf("expected gui/<uid> domain, got: %s", r.calls[0])
	}
}

func TestUninstallCallsBootout(t *testing.T) {
	r := &fakeRunner{}
	m := NewWithRunner(r)
	if err := m.Uninstall("/tmp/io.noo-noo.d.plist"); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if !strings.Contains(r.calls[0], "bootout") {
		t.Errorf("expected bootout, got: %s", r.calls[0])
	}
}
