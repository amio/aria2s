package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/amio/aria2s/internal/jobs"
	"github.com/amio/aria2s/internal/paths"
	"github.com/amio/aria2s/internal/state"
)

func TestStartupProgressTextRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "startup.progress")
	tests := []startupProgress{
		{phase: startupPhaseStarting},
		{phase: startupPhaseChecking, current: 3, total: 10},
		{phase: startupPhaseWaitingRPC},
	}
	for _, want := range tests {
		if err := writeStartupProgress(path, want); err != nil {
			t.Fatal(err)
		}
		got, err := readStartupProgress(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != want || got.message() == "" {
			t.Fatalf("progress got %#v (%q), want %#v", got, got.message(), want)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("progress mode = %o", info.Mode().Perm())
	}
}

func TestStartupProgressRejectsInvalidText(t *testing.T) {
	for _, value := range []string{"", "unknown\n", "checking\n", "checking 0 10\n", "checking 11 10\n", "checking x 10\n", "waiting-rpc extra\n"} {
		if _, err := parseStartupProgress([]byte(value)); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

func TestManagedExecReportsOnlyNaturalStartupBoundaries(t *testing.T) {
	application := managedExecTestApp(t, 2)
	oldPersist, oldExec, oldActivateLogs := persistStartupProgress, managedExec, activateManagedLogs
	defer func() {
		persistStartupProgress, managedExec, activateManagedLogs = oldPersist, oldExec, oldActivateLogs
	}()
	var got []startupProgress
	persistStartupProgress = func(_ string, progress startupProgress) error {
		got = append(got, progress)
		return nil
	}
	managedExec = func(string, []string, []string) error { return nil }
	activateManagedLogs = func(string, string) error { return nil }

	if err := application.ManagedExec(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []startupProgress{
		{phase: startupPhaseStarting},
		{phase: startupPhaseChecking, current: 1, total: 2},
		{phase: startupPhaseChecking, current: 2, total: 2},
		{phase: startupPhaseWaitingRPC},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("progress sequence = %#v, want %#v", got, want)
	}
}

func TestManagedExecProgressFailureIsBestEffortAndExecErrorCleansUp(t *testing.T) {
	application := managedExecTestApp(t, 0)
	oldPersist, oldClear, oldExec, oldActivateLogs := persistStartupProgress, clearStartupProgress, managedExec, activateManagedLogs
	defer func() {
		persistStartupProgress, clearStartupProgress, managedExec, activateManagedLogs = oldPersist, oldClear, oldExec, oldActivateLogs
	}()
	activateManagedLogs = func(string, string) error { return nil }
	persistStartupProgress = func(string, startupProgress) error { return errors.New("read-only state") }
	cleared := false
	clearStartupProgress = func(string) error {
		cleared = true
		return nil
	}
	execErr := errors.New("exec failed")
	managedExec = func(string, []string, []string) error { return execErr }

	if err := application.ManagedExec(context.Background()); !errors.Is(err, execErr) {
		t.Fatalf("ManagedExec error = %v", err)
	}
	if !cleared {
		t.Fatal("exec failure did not clear startup progress")
	}
}

func TestManagedExecStopsBeforeRuntimeWorkWhenLogActivationFails(t *testing.T) {
	application := managedExecTestApp(t, 0)
	oldExec, oldActivateLogs := managedExec, activateManagedLogs
	defer func() { managedExec, activateManagedLogs = oldExec, oldActivateLogs }()
	want := errors.New("log directory is read-only")
	activateManagedLogs = func(stdoutPath, stderrPath string) error {
		if stdoutPath != application.options.Paths.LogFile || stderrPath != application.options.Paths.ErrorLogFile {
			t.Fatalf("log paths = %q, %q", stdoutPath, stderrPath)
		}
		return want
	}
	managedExec = func(string, []string, []string) error {
		t.Fatal("aria2 exec reached after log activation failure")
		return nil
	}

	if err := application.ManagedExec(context.Background()); !errors.Is(err, want) {
		t.Fatalf("ManagedExec error = %v", err)
	}
}

func managedExecTestApp(t *testing.T, jobCount int) *App {
	t.Helper()
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	controller, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	controller, err = filepath.EvalSymlinks(controller)
	if err != nil {
		t.Fatal(err)
	}
	controllerIdentity, err := fileIdentity(controller)
	if err != nil {
		t.Fatal(err)
	}
	serviceData := []byte("test service\n")
	if err := os.MkdirAll(filepath.Dir(servicePaths.ServiceFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(servicePaths.ServiceFile, serviceData, 0o600); err != nil {
		t.Fatal(err)
	}
	current := state.State{
		RuntimeSchemaVersion: 2,
		ControllerPath:       controller,
		ControllerIdentity:   controllerIdentity,
		ServiceIdentity:      fmt.Sprintf("%x", sha256.Sum256(serviceData)),
		Aria2cPath:           controller,
		RPCPort:              6800,
		RPCSecret:            "secret",
		SessionPath:          servicePaths.SessionFile,
		StartupInputPath:     servicePaths.StartupInputFile,
		LogPath:              servicePaths.LogFile,
		ErrorLogPath:         servicePaths.ErrorLogFile,
		ServiceName:          servicePaths.ServiceName,
	}
	if err := state.Save(servicePaths.StateFile, current); err != nil {
		t.Fatal(err)
	}
	repository := jobs.New(servicePaths.StateDir)
	for index := range jobCount {
		id := fmt.Sprintf("%016x", index+1)
		job := jobs.Job{
			ID:             id,
			Source:         "https://example.test/file",
			TargetDir:      filepath.Join(root, "downloads"),
			TargetIdentity: jobs.ObjectIdentity{MountID: 1, ObjectID: 1},
			StorageID:      "1111111111111111",
			ActivityIntent: jobs.ActivityRunning,
			Payload:        jobs.PayloadState{Location: jobs.PayloadStaging},
		}
		if _, err := repository.Create(job); err != nil {
			t.Fatal(err)
		}
	}
	return New(Options{Paths: servicePaths})
}
