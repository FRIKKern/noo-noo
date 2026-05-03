package startup

import (
	"context"
	"strings"
	"testing"

	"github.com/FRIKKern/noo-noo/internal/modules"
)

func TestScanReportsConfiguredTargets(t *testing.T) {
	cfg := Config{
		UserAgents:    []string{"com.foo.user"},
		SystemAgents:  []string{"com.bar.agent"},
		SystemDaemons: []string{"com.baz.daemon"},
	}
	m := New(cfg, &FakeRunner{}, "/tmp/restore.log", 501)
	rep, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Items) != 3 {
		t.Errorf("expected 3 items, got %d", len(rep.Items))
	}
	if rep.Items[0].Evidence["domain"] != "gui/501" {
		t.Errorf("user agent should map to gui/501, got %q", rep.Items[0].Evidence["domain"])
	}
}

func TestApplyDisableInvokesLaunchctl(t *testing.T) {
	r := &FakeRunner{}
	cfg := Config{UserAgents: []string{"com.foo"}}
	m := New(cfg, r, "/tmp/restore.log", 501)
	rep, _ := m.Scan(context.Background())
	actions := m.Plan(rep)
	if len(actions) != 1 || actions[0].Op != "disable" {
		t.Fatalf("expected one disable action, got %+v", actions)
	}

	if _, err := m.Apply(context.Background(), actions[0]); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(r.Calls) != 2 {
		t.Fatalf("expected 2 launchctl calls (bootout + disable), got %d", len(r.Calls))
	}
	for i, want := range []string{"bootout", "disable"} {
		if r.Calls[i].Args[1] != want {
			t.Errorf("call[%d] = %v, want %s", i, r.Calls[i].Args, want)
		}
		if !strings.Contains(r.Calls[i].Args[2], "gui/501/com.foo") {
			t.Errorf("call[%d] target = %q, want contains gui/501/com.foo", i, r.Calls[i].Args[2])
		}
	}
}

func TestSystemAgentUsesSudoAndSystemDomain(t *testing.T) {
	r := &FakeRunner{}
	cfg := Config{SystemAgents: []string{"com.adobe.thing"}}
	m := New(cfg, r, "/tmp/restore.log", 501)
	rep, _ := m.Scan(context.Background())
	actions := m.Plan(rep)
	if _, err := m.Apply(context.Background(), actions[0]); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.Calls {
		if !c.Sudo {
			t.Errorf("system agent should use sudo: %+v", c)
		}
		if !strings.Contains(c.Args[2], "system/com.adobe.thing") {
			t.Errorf("expected system/com.adobe.thing target, got %q", c.Args[2])
		}
	}
}

func TestApplyEnableUndoes(t *testing.T) {
	r := &FakeRunner{}
	cfg := Config{UserAgents: []string{"com.foo"}}
	m := New(cfg, r, "/tmp/restore.log", 501)
	if _, err := m.Apply(context.Background(), modules.Action{
		Module: "startup", Op: "enable", Target: "gui/501/com.foo",
	}); err != nil {
		t.Fatal(err)
	}
	if len(r.Calls) != 1 || r.Calls[0].Args[1] != "enable" {
		t.Errorf("expected one enable call, got %+v", r.Calls)
	}
}
