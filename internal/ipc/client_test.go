package ipc

import (
	"path/filepath"
	"testing"
)

func TestClientDialMissingSocket(t *testing.T) {
	dir := t.TempDir()
	if _, err := Dial(filepath.Join(dir, "nope.sock")); err == nil {
		t.Error("Dial should fail when socket does not exist")
	}
}
