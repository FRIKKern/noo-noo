package menubar

import "testing"

type recHandler struct {
	scanNow, openSettings, quit int
	suggClicked                 []int
}

func (r *recHandler) OnScanNow()          { r.scanNow++ }
func (r *recHandler) OnOpenSettings()     { r.openSettings++ }
func (r *recHandler) OnQuit()             { r.quit++ }
func (r *recHandler) OnSuggestion(id int) { r.suggClicked = append(r.suggClicked, id) }

func TestClick_RoutesByID(t *testing.T) {
	h := &recHandler{}
	Dispatch(h, "scan-now")
	Dispatch(h, "settings")
	Dispatch(h, "quit")
	if h.scanNow != 1 || h.openSettings != 1 || h.quit != 1 {
		t.Errorf("handler counts = %+v", h)
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
	if h.scanNow+h.openSettings+h.quit+len(h.suggClicked) != 0 {
		t.Errorf("unknown IDs should be no-ops, got %+v", h)
	}
}
