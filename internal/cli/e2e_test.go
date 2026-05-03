package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEndToEndDevList(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "repo", "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "repo", "node_modules", "x.js"),
		make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	app := &App{Out: &out, Err: &errOut}
	code := app.Run(context.Background(),
		[]string{"noo-noo", "dev", "-roots", root, "list"})
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "node_modules") {
		t.Errorf("expected node_modules in output, got: %s", out.String())
	}
	if !strings.Contains(out.String(), "1.0 KB") {
		t.Errorf("expected size in output, got: %s", out.String())
	}
}

func TestEndToEndDevCleanRequiresConfirm(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "repo", "node_modules")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "x.js"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}

	// Without -y the prompt comes from os.Stdin which we can't drive in a
	// unit test cleanly. Use --dry-run + -y for a non-interactive path.
	var out, errOut bytes.Buffer
	app := &App{Out: &out, Err: &errOut}
	code := app.Run(context.Background(),
		[]string{"noo-noo", "dev", "-roots", root, "-y", "-dry-run", "clean"})
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "would delete") {
		t.Errorf("expected 'would delete' in dry-run output, got: %s", out.String())
	}
	// File must still exist.
	if _, err := os.Stat(target); err != nil {
		t.Errorf("dry-run should not have deleted: %v", err)
	}
}
