package runtime

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
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

func TestSafeStartupMarkerLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "safe-startup")
	if enabled, err := SafeStartupEnabled(path); err != nil || enabled {
		t.Fatalf("missing marker: enabled=%t err=%v", enabled, err)
	}
	if err := EnableSafeStartup(path); err != nil {
		t.Fatalf("enable safe startup: %v", err)
	}
	if enabled, err := SafeStartupEnabled(path); err != nil || !enabled {
		t.Fatalf("enabled marker: enabled=%t err=%v", enabled, err)
	}
	if err := DisableSafeStartup(path); err != nil {
		t.Fatalf("disable safe startup: %v", err)
	}
	if enabled, err := SafeStartupEnabled(path); err != nil || enabled {
		t.Fatalf("removed marker: enabled=%t err=%v", enabled, err)
	}
}

func TestSafeStartupMarkerRejectsUnexpectedContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "safe-startup")
	if err := os.WriteFile(path, []byte("file-allocation=prealloc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SafeStartupEnabled(path); err == nil {
		t.Fatal("unexpected safe-startup content was accepted")
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

func TestInheritedLockClosureDoesNotKeepLeaseAlive(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "instance.lock")
	lease, err := Acquire(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	// Acquire intentionally clears CLOEXEC for the managed aria2 syscall.Exec.
	// This test uses os/exec plus ExtraFiles, so suppress the otherwise duplicate
	// inherited descriptor and model the single FD that aria2 passes to a hook.
	if _, err := unix.FcntlInt(lease.file.Fd(), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestInheritedLockHelper$")
	command.Env = append(os.Environ(), "ARIA2S_LOCK_HELPER=1", LockFDEnvironment+"=3")
	command.ExtraFiles = []*os.File{lease.file}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
	}()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "closed" {
		t.Fatalf("lock helper did not close inherited descriptor: %q err=%v", scanner.Text(), scanner.Err())
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := Acquire(lockPath)
	if err != nil {
		t.Fatalf("child retained inherited lock after CloseInheritedLock: %v", err)
	}
	defer replacement.Close()
}

func TestInheritedLockHelper(t *testing.T) {
	if os.Getenv("ARIA2S_LOCK_HELPER") != "1" {
		return
	}
	if err := CloseInheritedLock(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stdout.WriteString("closed\n"); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}
