package menubar

import (
	"testing"
	"time"
)

func TestMenu_StructureWhenIdle(t *testing.T) {
	st := Status{Running: true, OpenSuggestions: 0, LastScanAt: time.Now()}
	m := Build(st, nil)
	wantIDs := []string{"status", "sep", "scan-now", "settings", "sep", "quit"}
	if got := ids(m); !equal(got, wantIDs) {
		t.Errorf("menu IDs = %v, want %v", got, wantIDs)
	}
}

func TestMenu_DaemonDownDisablesActions(t *testing.T) {
	st := Status{Running: false}
	m := Build(st, nil)
	for _, it := range m.Items {
		if it.ID == "scan-now" && !it.Disabled {
			t.Error("scan-now should be disabled when daemon is down")
		}
	}
}

func ids(m *Menu) []string {
	out := make([]string, 0, len(m.Items))
	for _, it := range m.Items {
		if it.Separator {
			out = append(out, "sep")
		} else {
			out = append(out, it.ID)
		}
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestMenu_Badge_DownLabel(t *testing.T) {
	m := Build(Status{Running: false}, nil)
	if m.Items[0].Label != "Daemon: down" {
		t.Errorf("down label = %q", m.Items[0].Label)
	}
}

func TestMenu_Badge_IdleLabel(t *testing.T) {
	m := Build(Status{Running: true, OpenSuggestions: 0}, nil)
	if m.Items[0].Label != "Daemon: up · idle" {
		t.Errorf("idle label = %q", m.Items[0].Label)
	}
}

func TestMenu_Badge_SingularVsPlural(t *testing.T) {
	cases := map[int]string{
		1: "Daemon: up · 1 tip",
		2: "Daemon: up · 2 tips",
		7: "Daemon: up · 7 tips",
	}
	for n, want := range cases {
		m := Build(Status{Running: true, OpenSuggestions: n}, nil)
		if m.Items[0].Label != want {
			t.Errorf("n=%d: label = %q, want %q", n, m.Items[0].Label, want)
		}
	}
}
