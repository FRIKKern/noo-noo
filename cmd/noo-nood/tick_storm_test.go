package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/modules"
)

// tickLiveChromeItems fabricates n LIVE Chrome code-sign clones (the shape
// of a relaunch loop: every clone is held open by the Chrome it belongs
// to). Paths do not exist on disk, so appearance time falls back to the
// first-sighting clock — exactly the founding-machine shape minus lsof.
func tickLiveChromeItems(prefix string, n int) []modules.Item {
	out := make([]modules.Item, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, modules.Item{
			Path: fmt.Sprintf("/private/var/folders/zz/T/X/%s-%02d.code_sign_clone/code_sign_clone.aaaaaa", prefix, i),
			Size: 1 << 30,
			Evidence: map[string]string{
				"signature":  "chrome-code-sign-clone",
				"staleness":  "live",
				"lsof":       "held open by Chrome",
				"workaround": tickTestWorkaround,
			},
		})
	}
	return out
}

// TestRunTickLeakStormNotifies is the founding-day replay: a burst of live
// Chrome clones yields ZERO suggestions (correct — they are live) but MUST
// yield one "leak storm" notification naming the signature, the count and
// the permanent fix; the alert is muted for 6 hours per signature after.
func TestRunTickLeakStormNotifies(t *testing.T) {
	d, _ := tickTestDaemon(t)
	d.cfg.Notify.Enabled = true
	clock := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return clock }
	rec := captureNotifications(t)

	// Nine clones: under the threshold, silence.
	src := &tickFakeLeakSource{items: tickLiveChromeItems("a", 9)}
	swapLeakSource(t, src)
	if err := d.RunTick(context.Background(), TriggerPressure); err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if len(rec.bodies) != 0 {
		t.Fatalf("9 new clones must not alert; got %q", rec.bodies)
	}

	// Twelve within the hour: storm.
	clock = clock.Add(10 * time.Minute)
	src.items = tickLiveChromeItems("a", 12)
	if err := d.RunTick(context.Background(), TriggerPressure); err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if len(rec.bodies) != 1 {
		t.Fatalf("notifications = %d, want exactly 1 storm alert; got %q", len(rec.bodies), rec.bodies)
	}
	if rec.titles[0] != "noo-noo — leak storm" {
		t.Errorf("title = %q", rec.titles[0])
	}
	body := rec.bodies[0]
	for _, want := range []string{
		"chrome-code-sign-clone",
		"12 new leak(s) in the last hour",
		"relaunching in a loop",
		"Permanent fix:",
		"--disable-features=MacAppCodeSignClone",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("storm body %q missing %q", body, want)
		}
	}
	if strings.Contains(body, "new suggestion(s)") {
		t.Errorf("storm alert must not use the generic suggestion copy: %q", body)
	}

	// Same storm five minutes later: muted by the 6h cooldown.
	clock = clock.Add(5 * time.Minute)
	if err := d.RunTick(context.Background(), TriggerPressure); err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if len(rec.bodies) != 1 {
		t.Fatalf("cooldown violated: %d notifications, want 1", len(rec.bodies))
	}

	// Seven hours on, the old clones are outside the window: no storm even
	// though they are all still present.
	clock = clock.Add(7 * time.Hour)
	if err := d.RunTick(context.Background(), TriggerPressure); err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if len(rec.bodies) != 1 {
		t.Fatalf("stale instances outside the window alerted: %d notifications", len(rec.bodies))
	}

	// A fresh burst after the cooldown alerts again.
	src.items = append(src.items, tickLiveChromeItems("b", 10)...)
	if err := d.RunTick(context.Background(), TriggerPressure); err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if len(rec.bodies) != 2 {
		t.Fatalf("second storm after cooldown: %d notifications, want 2", len(rec.bodies))
	}
	if !strings.Contains(rec.bodies[1], "10 new leak(s)") {
		t.Errorf("second storm body = %q", rec.bodies[1])
	}
}

// TestRunTickLeakStormRespectsNotifyDisabled: with notifications off the
// storm is logged but nothing is sent and the ledger stays empty.
func TestRunTickLeakStormRespectsNotifyDisabled(t *testing.T) {
	d, st := tickTestDaemon(t)
	rec := captureNotifications(t)
	swapLeakSource(t, &tickFakeLeakSource{items: tickLiveChromeItems("a", 15)})
	if err := d.RunTick(context.Background(), TriggerDaily); err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if len(rec.bodies) != 0 {
		t.Fatalf("notify disabled but sent %q", rec.bodies)
	}
	if _, fired, err := st.LastLeakStormAlert("chrome-code-sign-clone"); err != nil || fired {
		t.Errorf("ledger should be empty when nothing was sent (fired=%v err=%v)", fired, err)
	}
}
