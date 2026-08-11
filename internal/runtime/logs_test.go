package runtime_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	managedruntime "github.com/amio/aria2s/internal/runtime"
)

func TestActivateLogsBindsProcessOutput(t *testing.T) {
	if os.Getenv("ARIA2S_TEST_ACTIVATE_LOGS") == "1" {
		if err := managedruntime.ActivateLogs(os.Getenv("ARIA2S_TEST_STDOUT_LOG"), os.Getenv("ARIA2S_TEST_STDERR_LOG")); err != nil {
			os.Exit(2)
		}
		fmt.Fprint(os.Stdout, "managed stdout")
		fmt.Fprint(os.Stderr, "managed stderr")
		os.Exit(0)
	}
	root := t.TempDir()
	stdoutPath := filepath.Join(root, "aria2.log")
	stderrPath := filepath.Join(root, "aria2.err.log")
	command := exec.Command(os.Args[0], "-test.run=^TestActivateLogsBindsProcessOutput$")
	command.Env = append(os.Environ(),
		"ARIA2S_TEST_ACTIVATE_LOGS=1",
		"ARIA2S_TEST_STDOUT_LOG="+stdoutPath,
		"ARIA2S_TEST_STDERR_LOG="+stderrPath,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("log activation subprocess: %v: %s", err, output)
	}
	assertLog(t, stdoutPath, "managed stdout")
	assertLog(t, stderrPath, "managed stderr")
}

func TestActivateLogsRecordsFailureInErrorLog(t *testing.T) {
	root := t.TempDir()
	stdoutPath := filepath.Join(root, "aria2.log")
	if err := os.Mkdir(stdoutPath, 0o755); err != nil {
		t.Fatal(err)
	}
	stderrPath := filepath.Join(root, "aria2.err.log")
	if err := managedruntime.ActivateLogs(stdoutPath, stderrPath); err == nil {
		t.Fatal("accepted directory stdout log")
	}
	data, err := os.ReadFile(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" {
		t.Fatal("log activation failure was not recorded")
	}
}

func TestRotateLogKeepsActiveAndTwoRecentArchives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aria2.log")
	writeLog(t, path, "current!")
	writeLog(t, path+".1", "previous")
	writeLog(t, path+".2", "oldest")
	writeLog(t, path+".3", "stale")

	if err := managedruntime.RotateLog(path, int64(len("current!")), 3); err != nil {
		t.Fatal(err)
	}
	assertLog(t, path+".1", "current!")
	assertLog(t, path+".2", "previous")
	for _, removed := range []string{path, path + ".3"} {
		if _, err := os.Stat(removed); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected %s to be removed, got %v", removed, err)
		}
	}
}

func TestRotateLogLeavesBelowThresholdContentAndCleansExcessArchives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aria2.log")
	writeLog(t, path, "current")
	writeLog(t, path+".3", "stale")

	if err := managedruntime.RotateLog(path, 1024, 3); err != nil {
		t.Fatal(err)
	}
	assertLog(t, path, "current")
	if _, err := os.Stat(path + ".3"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale archive retained: %v", err)
	}
}

func TestRotateLogCapsOversizedActiveAndRetainedArchives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aria2.log")
	writeLog(t, path, "0123456789")
	writeLog(t, path+".1", "abcdefghij")

	if err := managedruntime.RotateLog(path, 6, 3); err != nil {
		t.Fatal(err)
	}
	assertLog(t, path+".1", "456789")
	assertLog(t, path+".2", "efghij")
}

func TestRotateLogAllowsMissingActiveFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aria2.log")
	if err := managedruntime.RotateLog(path, 1024, 3); err != nil {
		t.Fatal(err)
	}
}

func TestRotateLogRejectsSymlinksAndNonRegularFiles(t *testing.T) {
	root := t.TempDir()
	regular := filepath.Join(root, "regular")
	writeLog(t, regular, "content")
	symlink := filepath.Join(root, "aria2.log")
	if err := os.Symlink(regular, symlink); err != nil {
		t.Fatal(err)
	}
	if err := managedruntime.RotateLog(symlink, 1, 3); err == nil {
		t.Fatal("accepted symlink active log")
	}
	if err := os.Remove(symlink); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(symlink, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := managedruntime.RotateLog(symlink, 1, 3); err == nil {
		t.Fatal("accepted directory active log")
	}
}

func TestRotateLogRejectsUnsafeArchive(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "aria2.log")
	writeLog(t, path, "current")
	if err := os.Symlink(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := managedruntime.RotateLog(path, 1, 3); err == nil {
		t.Fatal("accepted symlink archive")
	}
}

func writeLog(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertLog(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}
