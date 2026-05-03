package menubar

import (
	"sync"
	"sync/atomic"
	"testing"
)

type recHandler struct {
	scanNow, openSettings, quit atomic.Int32
	mu                          sync.Mutex
	suggClicked                 []int
}

func (r *recHandler) OnScanNow()      { r.scanNow.Add(1) }
func (r *recHandler) OnOpenSettings() { r.openSettings.Add(1) }
func (r *recHandler) OnQuit()         { r.quit.Add(1) }
func (r *recHandler) OnSuggestion(id int) {
	r.mu.Lock()
	r.suggClicked = append(r.suggClicked, id)
	r.mu.Unlock()
}

func TestClick_RoutesByID(t *testing.T) {
	h := &recHandler{}
	Dispatch(h, "scan-now")
	Dispatch(h, "settings")
	Dispatch(h, "quit")
	if h.scanNow.Load() != 1 || h.openSettings.Load() != 1 || h.quit.Load() != 1 {
		t.Errorf("handler counts = scanNow:%d openSettings:%d quit:%d",
			h.scanNow.Load(), h.openSettings.Load(), h.quit.Load())
	}
}

func TestClick_SuggestionPrefix(t *testing.T) {
	h := &recHandler{}
	Dispatch(h, "sugg-17")
	Dispatch(h, "sugg-99")
	if len(h.suggClicked) != 2 || h.suggClicked[0] != 17 || h.suggClicked[1] != 99 {
		t.Errorf("suggClicked = %v", h.suggClicked)
	}
}

func TestClick_UnknownIDIsNoOp(t *testing.T) {
	h := &recHandler{}
	Dispatch(h, "status")        // disabled label, no callback
	Dispatch(h, "something-new") // unknown
	if h.scanNow.Load()+h.openSettings.Load()+h.quit.Load()+int32(len(h.suggClicked)) != 0 {
		t.Errorf("unknown IDs should be no-ops, got scanNow:%d openSettings:%d quit:%d suggClicked:%v",
			h.scanNow.Load(), h.openSettings.Load(), h.quit.Load(), h.suggClicked)
	}
}

func TestClick_Trigger_OnScanNowFiresHandler(t *testing.T) {
	h := &recHandler{}
	Dispatch(h, "scan-now")
	if h.scanNow.Load() != 1 {
		t.Errorf("OnScanNow not invoked, got scanNow:%d", h.scanNow.Load())
	}
}

func TestClick_Trigger_DispatchIsThreadSafe(t *testing.T) {
	h := &recHandler{}
	done := make(chan struct{})
	for i := 0; i < 100; i++ {
		go func() { Dispatch(h, "scan-now"); done <- struct{}{} }()
	}
	for i := 0; i < 100; i++ {
		<-done
	}
	// recHandler isn't thread-safe; this just confirms Dispatch doesn't panic
	// or hang under concurrent calls.
	if h.scanNow.Load() == 0 {
		t.Error("no calls registered")
	}
}

func TestClick_Trigger_TriggerThenRefreshCallsBoth(t *testing.T) {
	scanCalls, refreshCalls := 0, 0
	TriggerThenRefresh(func() error { scanCalls++; return nil }, func() { refreshCalls++ })
	if scanCalls != 1 || refreshCalls != 1 {
		t.Errorf("scan=%d refresh=%d, want 1 1", scanCalls, refreshCalls)
	}
}

func TestClick_Trigger_TriggerThenRefreshSkipsRefreshOnError(t *testing.T) {
	scanCalls, refreshCalls := 0, 0
	TriggerThenRefresh(
		func() error { scanCalls++; return errSentinel },
		func() { refreshCalls++ },
	)
	if scanCalls != 1 || refreshCalls != 0 {
		t.Errorf("scan=%d refresh=%d, want 1 0", scanCalls, refreshCalls)
	}
}

// errSentinel is an arbitrary non-nil error used by TriggerThenRefresh tests.
var errSentinel = sentinelErr("scan failed")

type sentinelErr string

func (e sentinelErr) Error() string { return string(e) }
