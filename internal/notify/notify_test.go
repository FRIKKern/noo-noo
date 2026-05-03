package notify

import (
	"strings"
	"testing"
)

type fakeRunner struct {
	name string
	args []string
}

func (f *fakeRunner) Run(name string, args ...string) ([]byte, error) {
	f.name = name
	f.args = args
	return nil, nil
}

func TestSendShellsOutToOsascript(t *testing.T) {
	r := &fakeRunner{}
	n := NewWithRunner(r)
	if err := n.Send("noo-noo", "3 idle repos hold 4.2 GB", "noo-noo://suggestions"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if r.name != "osascript" {
		t.Errorf("name = %q, want osascript", r.name)
	}
	joined := strings.Join(r.args, " ")
	if !strings.Contains(joined, "display notification") {
		t.Errorf("args missing display notification: %v", r.args)
	}
	if !strings.Contains(joined, "3 idle repos") {
		t.Errorf("args missing body text: %v", r.args)
	}
	if !strings.Contains(joined, "noo-noo") {
		t.Errorf("args missing title: %v", r.args)
	}
}

func TestSendEscapesQuotes(t *testing.T) {
	r := &fakeRunner{}
	n := NewWithRunner(r)
	if err := n.Send("title", `body with "quotes"`, ""); err != nil {
		t.Fatalf("Send: %v", err)
	}
	joined := strings.Join(r.args, " ")
	if strings.Contains(joined, `body with "quotes"`) {
		t.Errorf("quotes should be escaped: %v", r.args)
	}
	if !strings.Contains(joined, `\"quotes\"`) {
		t.Errorf("expected escaped quotes in args: %v", r.args)
	}
}
