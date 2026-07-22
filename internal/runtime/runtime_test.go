package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLeaseIsExclusiveAndHookExecsController(t *testing.T) {
	root := t.TempDir()
	lease, err := Acquire(filepath.Join(root, "instance.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if _, err := Acquire(filepath.Join(root, "instance.lock")); err == nil {
		t.Fatal("second lease acquired")
	}
	hook := filepath.Join(root, "hook")
	if err := WriteHook(hook, "/path/to/aria2s", "on-download-complete"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(hook)
	if !strings.Contains(string(data), "managed-hook") {
		t.Fatalf("hook = %s", data)
	}
}

func TestHookPreservesLiteralControllerPathAndArguments(t *testing.T) {
	root := t.TempDir()
	controller := filepath.Join(root, "controller $HOME `tick` 'quote'")
	if err := os.WriteFile(controller, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(root, "hook")
	if err := WriteHook(hook, controller, "on-download-complete"); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(hook, "0123456789abcdef").CombinedOutput()
	if err != nil {
		t.Fatalf("execute hook: %v: %s", err, output)
	}
	if got, want := string(output), "managed-hook\non-download-complete\n0123456789abcdef\n"; got != want {
		t.Fatalf("hook arguments = %q, want %q", got, want)
	}
}
