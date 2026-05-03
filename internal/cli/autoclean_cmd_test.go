package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// fakeRPC is a stand-in for *rpc.Client. It records the last call and
// returns either a canned err or fills the reply pointer if a hook is set.
type fakeRPC struct {
	lastMethod string
	lastReq    any
	err        error
	fillReply  func(reply any)
}

func (f *fakeRPC) Call(method string, req, reply any) error {
	f.lastMethod = method
	f.lastReq = req
	if f.err != nil {
		return f.err
	}
	if f.fillReply != nil {
		f.fillReply(reply)
	}
	return nil
}

func (f *fakeRPC) Close() error { return nil }

func dialFake(f *fakeRPC) func() (rpcCaller, error) {
	return func() (rpcCaller, error) { return f, nil }
}

func TestEnableRequiresAckFlag(t *testing.T) {
	var buf bytes.Buffer
	f := &fakeRPC{}
	err := AutoCleanCmd(&buf, dialFake(f), []string{"enable"})
	if err == nil {
		t.Fatal("enable without --i-understand-the-risks must error")
	}
	if !strings.Contains(err.Error(), "i-understand-the-risks") {
		t.Errorf("error must name the flag, got: %v", err)
	}
	if f.lastMethod != "" {
		t.Errorf("must NOT call IPC if ack is missing, got: %q", f.lastMethod)
	}
}

func TestEnableWithAckCallsToggle(t *testing.T) {
	var buf bytes.Buffer
	f := &fakeRPC{}
	err := AutoCleanCmd(&buf, dialFake(f), []string{"enable", "--i-understand-the-risks"})
	if err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "AutoClean.Toggle" {
		t.Errorf("wrong method: %q", f.lastMethod)
	}
	out := buf.String()
	if !strings.Contains(out, "auto-clean enabled") {
		t.Errorf("missing success line; got: %q", out)
	}
}

func TestDisableCallsToggleNoFlag(t *testing.T) {
	var buf bytes.Buffer
	f := &fakeRPC{}
	if err := AutoCleanCmd(&buf, dialFake(f), []string{"disable"}); err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "AutoClean.Toggle" {
		t.Errorf("wrong method: %q", f.lastMethod)
	}
	if !strings.Contains(buf.String(), "disabled") {
		t.Errorf("missing disabled line; got: %q", buf.String())
	}
}

func TestStatusCallsStatus(t *testing.T) {
	var buf bytes.Buffer
	f := &fakeRPC{}
	if err := AutoCleanCmd(&buf, dialFake(f), []string{"status"}); err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "AutoClean.Status" {
		t.Errorf("wrong method: %q", f.lastMethod)
	}
}

func TestHistoryCallsStatusWithLimit(t *testing.T) {
	var buf bytes.Buffer
	f := &fakeRPC{}
	if err := AutoCleanCmd(&buf, dialFake(f), []string{"history", "--limit", "5"}); err != nil {
		t.Fatal(err)
	}
	if f.lastMethod != "AutoClean.Status" {
		t.Errorf("wrong method: %q", f.lastMethod)
	}
}

func TestUnknownVerbErrors(t *testing.T) {
	var buf bytes.Buffer
	f := &fakeRPC{}
	err := AutoCleanCmd(&buf, dialFake(f), []string{"frobnicate"})
	if err == nil {
		t.Fatal("expected error on unknown verb")
	}
	if !strings.Contains(err.Error(), "unknown verb") {
		t.Errorf("expected 'unknown verb' in error, got: %v", err)
	}
}

func TestNoArgsErrors(t *testing.T) {
	var buf bytes.Buffer
	f := &fakeRPC{}
	err := AutoCleanCmd(&buf, dialFake(f), nil)
	if err == nil {
		t.Fatal("expected error with no verb")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Errorf("expected usage in error, got: %v", err)
	}
}

func TestRPCErrorBubblesUp(t *testing.T) {
	var buf bytes.Buffer
	f := &fakeRPC{err: errors.New("boom")}
	err := AutoCleanCmd(&buf, dialFake(f), []string{"status"})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected rpc error to bubble up, got: %v", err)
	}
}

func TestYnStrAndIfBlank(t *testing.T) {
	if ynStr(true) != "enabled" {
		t.Errorf("ynStr(true) = %q", ynStr(true))
	}
	if ynStr(false) != "disabled" {
		t.Errorf("ynStr(false) = %q", ynStr(false))
	}
	if ifBlank("", "alt") != "alt" {
		t.Errorf("ifBlank empty failed")
	}
	if ifBlank("x", "alt") != "x" {
		t.Errorf("ifBlank nonempty failed")
	}
}
