package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/ipc"
)

type fakeStatusClient struct {
	resp        ipc.StatusResponse
	err         error
	called      bool
	triggerResp ipc.TriggerScanReply
	triggerErr  error
	triggerHit  bool
}

func (f *fakeStatusClient) DaemonStatus() (ipc.StatusResponse, error) {
	f.called = true
	return f.resp, f.err
}
func (f *fakeStatusClient) TriggerScan() (ipc.TriggerScanReply, error) {
	f.triggerHit = true
	return f.triggerResp, f.triggerErr
}
func (f *fakeStatusClient) Close() error { return nil }

type fakeRunner struct {
	calls []string
	err   error
}

func (r *fakeRunner) Run(name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	return nil, r.err
}

func TestDaemonCmdStartCallsLaunchctlLoad(t *testing.T) {
	r := &fakeRunner{}
	out := &bytes.Buffer{}
	cmd := newDaemonCmd(daemonOpts{
		PlistPath: "/tmp/io.noo-noo.d.plist",
		Runner:    r,
		Out:       out,
	})
	if err := cmd.Run([]string{"start"}); err != nil {
		t.Fatalf("Run start: %v", err)
	}
	if len(r.calls) == 0 || !strings.Contains(r.calls[0], "load") {
		t.Errorf("expected launchctl load, got %v", r.calls)
	}
}

func TestDaemonCmdStopCallsLaunchctlUnload(t *testing.T) {
	r := &fakeRunner{}
	cmd := newDaemonCmd(daemonOpts{PlistPath: "/tmp/x.plist", Runner: r, Out: &bytes.Buffer{}})
	if err := cmd.Run([]string{"stop"}); err != nil {
		t.Fatalf("Run stop: %v", err)
	}
	if len(r.calls) == 0 || !strings.Contains(r.calls[0], "unload") {
		t.Errorf("expected launchctl unload, got %v", r.calls)
	}
}

func TestDaemonCmdStatusReportsRunning(t *testing.T) {
	fc := &fakeStatusClient{resp: ipc.StatusResponse{Running: true, Uptime: 90 * time.Minute, Version: "0.2.0"}}
	out := &bytes.Buffer{}
	cmd := newDaemonCmd(daemonOpts{
		Dial: func() (statusClient, error) { return fc, nil },
		Out:  out,
	})
	if err := cmd.Run([]string{"status"}); err != nil {
		t.Fatalf("Run status: %v", err)
	}
	if !fc.called {
		t.Error("expected DaemonStatus to be called")
	}
	if !strings.Contains(out.String(), "running") {
		t.Errorf("output missing 'running': %s", out.String())
	}
}

func TestDaemonCmdStatusReportsDown(t *testing.T) {
	cmd := newDaemonCmd(daemonOpts{
		Dial: func() (statusClient, error) { return nil, errors.New("dial unix: connection refused") },
		Out:  &bytes.Buffer{},
	})
	err := cmd.Run([]string{"status"})
	if err == nil {
		t.Error("expected status error when daemon is down")
	}
}

func TestDaemonCmdForceScanCallsTrigger(t *testing.T) {
	fc := &fakeStatusClient{
		triggerResp: ipc.TriggerScanReply{Ok: true, SuggestionsAdded: 3, DurationMs: 42},
	}
	out := &bytes.Buffer{}
	cmd := newDaemonCmd(daemonOpts{
		Dial: func() (statusClient, error) { return fc, nil },
		Out:  out,
	})
	if err := cmd.Run([]string{"force-scan"}); err != nil {
		t.Fatalf("Run force-scan: %v", err)
	}
	if !fc.triggerHit {
		t.Error("expected TriggerScan to be called")
	}
	if !strings.Contains(out.String(), "suggestions_added=3") {
		t.Errorf("output missing 'suggestions_added=3': %s", out.String())
	}
	_ = time.Now() // keep "time" import
}
