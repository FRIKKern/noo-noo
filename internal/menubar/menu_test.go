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

func TestMenu_Submenu_RendersTopThree(t *testing.T) {
	suggs := []Suggestion{
		{ID: 17, Module: "dev", Reason: "3 idle repos", SizeBytes: 4_200_000_000, Severity: "medium"},
		{ID: 18, Module: "caches", Reason: "yarn cache", SizeBytes: 1_100_000_000, Severity: "low"},
		{ID: 19, Module: "caches", Reason: "pnpm cache", SizeBytes: 800_000_000, Severity: "low"},
		{ID: 20, Module: "caches", Reason: "npm cache", SizeBytes: 250_000_000, Severity: "low"},
	}
	m := Build(Status{Running: true, OpenSuggestions: 4}, suggs)
	var sub *Menu
	for _, it := range m.Items {
		if it.ID == "suggestions" {
			sub = it.Submenu
		}
	}
	if sub == nil {
		t.Fatal("suggestions submenu missing")
	}
	// 3 inline suggestions + 1 "see all" trailer = 4 items.
	if len(sub.Items) != 4 {
		t.Fatalf("submenu items = %d, want 4", len(sub.Items))
	}
	if sub.Items[0].ID != "sugg-17" {
		t.Errorf("first sub item ID = %q, want sugg-17", sub.Items[0].ID)
	}
	if sub.Items[3].ID != "see-all" {
		t.Errorf("trailer ID = %q, want see-all", sub.Items[3].ID)
	}
}

func TestMenu_Submenu_HiddenWhenEmpty(t *testing.T) {
	m := Build(Status{Running: true, OpenSuggestions: 0}, nil)
	for _, it := range m.Items {
		if it.ID == "suggestions" {
			t.Error("suggestions item should be hidden when none open")
		}
	}
}
