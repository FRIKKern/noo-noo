package startup

import (
	"context"
	"errors"
	"testing"
)

func TestFakeRunnerRecordsCalls(t *testing.T) {
	r := &FakeRunner{}
	if err := r.Run(context.Background(), false, "launchctl", "disable", "system/x"); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), true, "launchctl", "bootout", "system/x"); err != nil {
		t.Fatal(err)
	}
	if len(r.Calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(r.Calls))
	}
	if r.Calls[1].Sudo != true || r.Calls[1].Args[1] != "bootout" {
		t.Errorf("call[1] mismatch: %+v", r.Calls[1])
	}
}

func TestFakeRunnerCanReturnError(t *testing.T) {
	r := &FakeRunner{Err: errors.New("boom")}
	if err := r.Run(context.Background(), false, "launchctl", "disable", "x"); err == nil {
		t.Error("expected error")
	}
}
