package menubar

import "testing"

func TestIcon_DefaultIsNotEmpty(t *testing.T) {
	icon := Default()
	if len(icon.PNG) == 0 {
		t.Fatal("Default icon PNG is empty")
	}
	if !icon.Template {
		t.Errorf("Default icon should be a template (renders in both menubar modes)")
	}
}

func TestIcon_AllStatesPresent(t *testing.T) {
	for _, s := range []State{StateIdle, StateScanning, StateHasSuggestions} {
		icon := ForState(s)
		if len(icon.PNG) == 0 {
			t.Errorf("state %v: PNG empty", s)
		}
	}
}

func TestIcon_ScanningDiffersFromIdle(t *testing.T) {
	if string(ForState(StateScanning).PNG) == string(ForState(StateIdle).PNG) {
		t.Error("Scanning and Idle icons should differ")
	}
}
