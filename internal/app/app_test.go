package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/amio/aria2s/internal/app"
	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/paths"
	"github.com/amio/aria2s/internal/service"
	"github.com/amio/aria2s/internal/state"
)

func TestInstallStartPollsRPCUntilReady(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	serviceBackend := &recordingService{}
	rpc := &flakyRPC{failuresRemaining: 2, version: "1.37.0"}
	application := newTestApp(servicePaths, aria2c, serviceBackend, rpc, app.Options{
		RPCReadyTimeout: time.Second,
		RPCPollInterval: time.Nanosecond,
	})

	if err := application.Install(context.Background(), true); err != nil {
		t.Fatalf("install --start should poll until RPC is ready: %v", err)
	}
	if rpc.versionCalls != 3 {
		t.Fatalf("expected 3 version attempts, got %d", rpc.versionCalls)
	}
}

func TestRecoverRPCRequiresAcknowledgementAndVerifiesSafeRestart(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	writeInstalledStateAndConfig(t, servicePaths, aria2c)
	serviceBackend := &recoveryService{running: true, loaded: true, marker: servicePaths.SafeStartupFile}
	rpc := &recoveryRPC{service: serviceBackend}
	application := newTestApp(servicePaths, aria2c, serviceBackend, rpc, app.Options{
		RPCReadyTimeout: time.Second,
		RPCPollInterval: time.Nanosecond,
	})

	if err := application.RecoverRPC(context.Background(), false); err == nil || !strings.Contains(err.Error(), "--discard-unmanaged-tasks") {
		t.Fatalf("expected unmanaged-task acknowledgement, got %v", err)
	}
	if len(serviceBackend.calls) != 0 {
		t.Fatalf("service changed before acknowledgement: %v", serviceBackend.calls)
	}
	if err := application.RecoverRPC(context.Background(), true); err != nil {
		t.Fatalf("recover RPC: %v", err)
	}
	if got := strings.Join(serviceBackend.calls, ","); got != "stop,start" {
		t.Fatalf("recovery service calls = %s", got)
	}
	if !serviceBackend.markerSeen {
		t.Fatal("service started without safe-startup marker")
	}
	if _, err := os.Stat(servicePaths.SafeStartupFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful recovery retained marker: %v", err)
	}
}

func TestRecoverRPCRetainsSafeMarkerUntilRPCResponds(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	writeInstalledStateAndConfig(t, servicePaths, aria2c)
	serviceBackend := &recoveryService{running: true, loaded: true, marker: servicePaths.SafeStartupFile}
	application := newTestApp(servicePaths, aria2c, serviceBackend, alwaysUnavailableRPC{}, app.Options{
		RPCReadyTimeout: time.Nanosecond,
		RPCPollInterval: time.Nanosecond,
	})

	if err := application.RecoverRPC(context.Background(), true); err == nil {
		t.Fatal("expected recovery verification failure")
	}
	if _, err := os.Stat(servicePaths.SafeStartupFile); err != nil {
		t.Fatalf("failed recovery did not retain safe marker: %v", err)
	}
}

func TestInstallStartPollsRPCUntilReadyOnLinuxPaths(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewLinux(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	serviceBackend := &recordingService{}
	rpc := &flakyRPC{failuresRemaining: 2, version: "1.37.0"}
	application := newTestApp(servicePaths, aria2c, serviceBackend, rpc, app.Options{
		RPCReadyTimeout: time.Second,
		RPCPollInterval: time.Nanosecond,
	})

	if err := application.Install(context.Background(), true); err != nil {
		t.Fatalf("install --start should poll until RPC is ready on Linux paths: %v", err)
	}
	if rpc.versionCalls != 3 {
		t.Fatalf("expected 3 version attempts, got %d", rpc.versionCalls)
	}
}

func TestInstallStartTimeoutGivesRecoveryGuidance(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	serviceBackend := &recordingService{}
	rpc := &flakyRPC{failuresRemaining: 100, version: "1.37.0"}
	application := newTestApp(servicePaths, aria2c, serviceBackend, rpc, app.Options{
		RPCReadyTimeout: time.Nanosecond,
		RPCPollInterval: time.Nanosecond,
		IsPortAvailable: func(int) bool {
			return true
		},
	})

	err := application.Install(context.Background(), true)

	if err == nil {
		t.Fatal("expected install --start timeout error")
	}
	message := err.Error()
	assertContains(t, message, "aria2 did not become reachable")
	assertContains(t, message, "http://127.0.0.1:6800/jsonrpc")
	assertContains(t, message, servicePaths.LogFile)
	assertContains(t, message, "aria2s doctor")
}

func TestStartPreflightsStateConfigAndWaitsForRPC(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	writeInstalledStateAndConfig(t, servicePaths, aria2c)
	serviceBackend := &recordingService{}
	rpc := &flakyRPC{failuresRemaining: 1, version: "1.37.0"}
	application := newTestApp(servicePaths, aria2c, serviceBackend, rpc, app.Options{
		RPCReadyTimeout: time.Second,
		RPCPollInterval: time.Nanosecond,
	})
	if err := os.WriteFile(servicePaths.StartupProgressFile, []byte("waiting-rpc\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := application.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	if strings.Join(serviceBackend.calls, ",") != "start" {
		t.Fatalf("expected start call, got %v", serviceBackend.calls)
	}
	if rpc.versionCalls != 2 {
		t.Fatalf("expected RPC readiness polling, got %d calls", rpc.versionCalls)
	}
	if _, err := os.Lstat(servicePaths.StartupProgressFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful start left startup progress: %v", err)
	}
}

func TestStartSkipsServiceStartWhenAlreadyRunningAndRPCHealthy(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	writeInstalledStateAndConfig(t, servicePaths, aria2c)
	serviceBackend := &recordingService{loaded: true, running: true}
	rpc := &flakyRPC{version: "1.37.0"}
	application := newTestApp(servicePaths, aria2c, serviceBackend, rpc, app.Options{
		RPCReadyTimeout: time.Second,
		RPCPollInterval: time.Nanosecond,
	})

	if err := application.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(serviceBackend.calls) != 0 {
		t.Fatalf("expected start to short-circuit, got service calls %v", serviceBackend.calls)
	}
	if rpc.versionCalls != 1 {
		t.Fatalf("expected one RPC health check, got %d", rpc.versionCalls)
	}
}

func TestStartFailsWhenStoredAria2cIsMissing(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := filepath.Join(root, "missing-aria2c")
	writeInstalledStateAndConfig(t, servicePaths, aria2c)
	serviceBackend := &recordingService{}
	application := newTestApp(servicePaths, aria2c, serviceBackend, fixedRPC{version: "1.37.0"}, app.Options{})

	err := application.Start(context.Background())

	if err == nil {
		t.Fatal("expected missing stored aria2c path to fail")
	}
	assertContains(t, err.Error(), "stored aria2c path is not executable")
	if len(serviceBackend.calls) != 0 {
		t.Fatalf("expected no service calls, got %v", serviceBackend.calls)
	}
}

func TestPrepareDashboardIgnoresMissingUserConfigAndRPC(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	writeInstalledStateAndConfig(t, servicePaths, aria2c)
	if err := touch0600ForTest(servicePaths.SessionFile); err != nil {
		t.Fatalf("touch session: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(servicePaths.LogFile), 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	current, err := state.Load(servicePaths.StateFile)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	plist, err := service.RenderLaunchAgent(current)
	if err != nil {
		t.Fatalf("render plist: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(servicePaths.ServiceFile), 0o755); err != nil {
		t.Fatalf("mkdir service dir: %v", err)
	}
	if err := os.WriteFile(servicePaths.ServiceFile, []byte(plist), 0o644); err != nil {
		t.Fatalf("write plist: %v", err)
	}
	serviceBackend := &recordingService{loaded: true, running: true}
	rpc := &flakyRPC{version: "1.37.0"}
	application := newTestApp(servicePaths, aria2c, serviceBackend, rpc, app.Options{
		RPCReadyTimeout: time.Second,
		RPCPollInterval: time.Nanosecond,
	})

	if _, err := application.PrepareDashboard(context.Background()); err != nil {
		t.Fatalf("prepare dashboard: %v", err)
	}
	if len(serviceBackend.calls) != 0 {
		t.Fatalf("expected no service calls, got %v", serviceBackend.calls)
	}
	if rpc.versionCalls != 0 {
		t.Fatalf("dashboard preparation must not probe RPC, got %d", rpc.versionCalls)
	}
}

func TestPrepareDashboardStartsValidInstalledServiceWithoutReinstalling(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	current := writeInstalledStateAndConfig(t, servicePaths, aria2c)
	if err := touch0600ForTest(servicePaths.SessionFile); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(servicePaths.LogFile), 0o755); err != nil {
		t.Fatal(err)
	}
	serviceFile, err := service.RenderLaunchAgent(current)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(servicePaths.ServiceFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(servicePaths.ServiceFile, []byte(serviceFile), 0o644); err != nil {
		t.Fatal(err)
	}
	serviceBackend := &recordingService{loaded: false, running: false}
	application := newTestApp(servicePaths, aria2c, serviceBackend, &flakyRPC{}, app.Options{})

	if _, err := application.PrepareDashboard(context.Background()); err != nil {
		t.Fatalf("prepare Dashboard: %v", err)
	}
	if !slices.Equal(serviceBackend.calls, []string{"start"}) {
		t.Fatalf("Dashboard service calls = %v, want [start]", serviceBackend.calls)
	}
}

func TestPrepareDashboardRejectsAlteredServiceWithoutMutation(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	writeInstalledStateAndConfig(t, servicePaths, aria2c)
	if err := touch0600ForTest(servicePaths.SessionFile); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(servicePaths.LogFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(servicePaths.ServiceFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(servicePaths.ServiceFile, []byte("stale service"), 0o644); err != nil {
		t.Fatal(err)
	}
	stateStamp := fileModTime(t, servicePaths.StateFile)
	serviceStamp := fileModTime(t, servicePaths.ServiceFile)
	serviceBackend := &recordingService{loaded: true, running: false}
	rpc := &flakyRPC{failuresRemaining: 100}
	application := newTestApp(servicePaths, aria2c, serviceBackend, rpc, app.Options{})

	_, err := application.PrepareDashboard(context.Background())
	if err == nil {
		t.Fatal("altered service artifact was accepted")
	}
	assertContains(t, err.Error(), "InstallIncomplete: service artifact identity does not match committed state")
	assertContains(t, err.Error(), "run `aria2s install`")
	if rpc.versionCalls != 0 {
		t.Fatalf("validation path probed RPC %d times", rpc.versionCalls)
	}
	if len(serviceBackend.calls) != 0 || serviceBackend.running {
		t.Fatalf("Dashboard mutated service: calls=%v running=%v", serviceBackend.calls, serviceBackend.running)
	}
	if got := fileModTime(t, servicePaths.StateFile); !got.Equal(stateStamp) {
		t.Fatalf("Dashboard rewrote state: got %s want %s", got, stateStamp)
	}
	if got := fileModTime(t, servicePaths.ServiceFile); !got.Equal(serviceStamp) {
		t.Fatalf("Dashboard rewrote service artifact: got %s want %s", got, serviceStamp)
	}
}

func TestPrepareDashboardTrustsCommittedServiceAcrossCLIVersions(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	current := writeInstalledStateAndConfig(t, servicePaths, aria2c)
	if err := touch0600ForTest(servicePaths.SessionFile); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(servicePaths.LogFile), 0o755); err != nil {
		t.Fatal(err)
	}
	installedService := []byte("service artifact authored by an older CLI")
	if err := os.MkdirAll(filepath.Dir(servicePaths.ServiceFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(servicePaths.ServiceFile, installedService, 0o644); err != nil {
		t.Fatal(err)
	}
	serviceHash := sha256.Sum256(installedService)
	current.ServiceIdentity = hex.EncodeToString(serviceHash[:])
	if err := state.Save(servicePaths.StateFile, current); err != nil {
		t.Fatal(err)
	}
	renderCalls := 0
	serviceBackend := &recordingService{loaded: true, running: false}
	application := newTestApp(servicePaths, aria2c, serviceBackend, &flakyRPC{}, app.Options{
		RenderService: func(state.State) (string, error) {
			renderCalls++
			return "different service artifact from the current CLI", nil
		},
	})

	if _, err := application.PrepareDashboard(context.Background()); err != nil {
		t.Fatalf("prepare Dashboard with an older committed service: %v", err)
	}
	if renderCalls != 0 {
		t.Fatalf("Dashboard re-derived installed service metadata %d times", renderCalls)
	}
	if !slices.Equal(serviceBackend.calls, []string{"start"}) {
		t.Fatalf("Dashboard service calls = %v, want [start]", serviceBackend.calls)
	}
}

func TestPrepareDashboardUsesRunningAria2cAfterControllerReplacement(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	current := writeInstalledStateAndConfig(t, servicePaths, aria2c)
	controller := writeExecutable(t, filepath.Join(root, "bin", "aria2s-controller"))
	controllerData, err := os.ReadFile(controller)
	if err != nil {
		t.Fatal(err)
	}
	controllerHash := sha256.Sum256(controllerData)
	current.ControllerPath = controller
	current.ControllerIdentity = hex.EncodeToString(controllerHash[:])
	if err := state.Save(servicePaths.StateFile, current); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(controller, []byte("replacement controller"), 0o755); err != nil {
		t.Fatal(err)
	}
	serviceBackend := &recordingService{loaded: true, running: true}
	renderCalls := 0
	application := newTestApp(servicePaths, aria2c, serviceBackend, &flakyRPC{}, app.Options{
		RenderService: func(state.State) (string, error) {
			renderCalls++
			return "replacement service", nil
		},
	})

	if _, err := application.PrepareDashboard(context.Background()); err != nil {
		t.Fatalf("prepare Dashboard against running aria2c: %v", err)
	}
	if renderCalls != 0 {
		t.Fatalf("Dashboard rendered service metadata %d times", renderCalls)
	}
	if len(serviceBackend.calls) != 0 || !serviceBackend.running {
		t.Fatalf("Dashboard disturbed running aria2c: calls=%v running=%v", serviceBackend.calls, serviceBackend.running)
	}
}

func TestPrepareDashboardReportsStoppedLegacyRuntimeReadyToInstall(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	if err := state.Save(servicePaths.StateFile, state.State{RuntimeSchemaVersion: 1}); err != nil {
		t.Fatalf("save legacy state: %v", err)
	}
	application := app.New(app.Options{Paths: servicePaths, Service: &recordingService{}})

	_, err := application.PrepareDashboard(context.Background())

	if err == nil {
		t.Fatal("expected legacy runtime to block Dashboard")
	}
	message := err.Error()
	assertContains(t, message, "managed runtime v1 is stopped and has no saved tasks; v2 is ready to install\n\n")
	assertContains(t, message, "The v1 service is stopped and its saved session is empty.\n")
	assertContains(t, message, "\nInstall and start the latest version:\n")
	assertContains(t, message, "curl -fsSL https://raw.githubusercontent.com/amio/aria2s/main/install.sh | sh")
	if strings.Contains(message, "--discard-legacy-tasks") {
		t.Fatalf("ready legacy runtime offered discard path: %s", message)
	}
}

func TestPrepareDashboardReportsSavedLegacyTasksAfterStop(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	if err := state.Save(servicePaths.StateFile, state.State{RuntimeSchemaVersion: 1}); err != nil {
		t.Fatalf("save legacy state: %v", err)
	}
	if err := os.WriteFile(servicePaths.LegacySessionFile, []byte("legacy task\n"), 0o600); err != nil {
		t.Fatalf("save legacy session: %v", err)
	}
	application := app.New(app.Options{Paths: servicePaths, Service: &recordingService{}})

	_, err := application.PrepareDashboard(context.Background())

	if err == nil {
		t.Fatal("expected saved legacy tasks to block Dashboard")
	}
	message := err.Error()
	assertContains(t, message, "LegacySessionPresent: managed runtime v1 is stopped, but aria2 retained saved restart entries\n\n")
	assertContains(t, message, "The stopped v1 service still has saved restart entries. V2 cannot determine\nwhether they are unfinished tasks or stale entries retained by aria2 after\nDashboard cleanup.\n")
	assertContains(t, message, "\nOption 1 — The v1 Dashboard was empty before stop\n")
	assertContains(t, message, "\nUsing the v2 binary that printed this message, run:\n  aria2s install --discard-legacy-tasks\n")
	assertContains(t, message, "\nThis confirms that the legacy restart entries can be ignored. It does not\ndelete legacy files or downloaded payloads.\n")
	assertContains(t, message, "\nOption 2 — Inspect or keep existing tasks\n\nInstall the last v1 release:\n")
	assertContains(t, message, "curl -fsSL https://raw.githubusercontent.com/amio/aria2s/main/install.sh | sh -s -- --version v0.4.0\n")
	assertContains(t, message, "\nInspect or finish the tasks, then stop v1:\n  aria2s dashboard\n  aria2s stop\n")
	assertContains(t, message, "\nIf the Dashboard was empty but aria2 retained restart entries, use Option 1.")
}

func TestPrepareDashboardReportsRunningLegacyRuntime(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	if err := state.Save(servicePaths.StateFile, state.State{RuntimeSchemaVersion: 1}); err != nil {
		t.Fatalf("save legacy state: %v", err)
	}
	application := app.New(app.Options{
		Paths:   servicePaths,
		Service: &recordingService{loaded: true, running: true},
	})

	_, err := application.PrepareDashboard(context.Background())

	if err == nil {
		t.Fatal("expected running legacy runtime to block Dashboard")
	}
	assertContains(t, err.Error(), "UpgradeRequired: managed runtime v1 is still running\n\n")
}

func TestStopSavesSessionBeforeStoppingService(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	writeInstalledStateAndConfig(t, servicePaths, aria2c)
	events := []string{}
	serviceBackend := &recordingService{loaded: true, running: true, events: &events}
	rpc := &sessionRecordingRPC{events: &events, service: serviceBackend}
	application := newTestApp(servicePaths, aria2c, serviceBackend, rpc, app.Options{})

	if err := application.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}

	if rpc.saveSessionCalls != 1 {
		t.Fatalf("expected one saveSession call, got %d", rpc.saveSessionCalls)
	}
	if strings.Join(events, ",") != "saveSession,stop" {
		t.Fatalf("expected saveSession then stop, got %v", events)
	}
}

func TestStopSavesSessionBeforeStoppingServiceOnLinuxPaths(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewLinux(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	writeInstalledStateAndConfig(t, servicePaths, aria2c)
	events := []string{}
	serviceBackend := &recordingService{loaded: true, running: true, events: &events}
	rpc := &sessionRecordingRPC{events: &events, service: serviceBackend}
	application := newTestApp(servicePaths, aria2c, serviceBackend, rpc, app.Options{})

	if err := application.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}

	if rpc.saveSessionCalls != 1 {
		t.Fatalf("expected one saveSession call, got %d", rpc.saveSessionCalls)
	}
	if strings.Join(events, ",") != "saveSession,stop" {
		t.Fatalf("expected saveSession then stop, got %v", events)
	}
}

func TestStopCallsServiceStopEvenWhenSaveSessionFails(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	writeInstalledStateAndConfig(t, servicePaths, aria2c)
	serviceBackend := &recordingService{loaded: true, running: true}
	rpc := &sessionRecordingRPC{saveSessionErr: errors.New("rpc unavailable")}
	application := newTestApp(servicePaths, aria2c, serviceBackend, rpc, app.Options{})

	err := application.Stop(context.Background())

	// Non-transport errors should be reported so callers know session save failed.
	if err == nil {
		t.Fatal("expected stop to report save session error")
	}
	assertContains(t, err.Error(), "save session")
	// Service.Stop() is always called as the definitive stop.
	if len(serviceBackend.calls) != 1 || serviceBackend.calls[0] != "stop" {
		t.Fatalf("expected service stop call after saveSession failure, got %v", serviceBackend.calls)
	}
}

func TestRestartSavesSessionBeforeRestartingService(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	writeInstalledStateAndConfig(t, servicePaths, aria2c)
	events := []string{}
	serviceBackend := &recordingService{loaded: true, running: true, events: &events}
	rpc := &sessionRecordingRPC{events: &events, service: serviceBackend}
	application := newTestApp(servicePaths, aria2c, serviceBackend, rpc, app.Options{
		RPCReadyTimeout: time.Second,
		RPCPollInterval: time.Nanosecond,
	})

	if err := application.Restart(context.Background()); err != nil {
		t.Fatalf("restart: %v", err)
	}

	if rpc.saveSessionCalls != 1 {
		t.Fatalf("expected one saveSession call, got %d", rpc.saveSessionCalls)
	}
	if strings.Join(events, ",") != "saveSession,stop,start,version" {
		t.Fatalf("expected saveSession, stop, start, version poll, got %v", events)
	}
}

func TestRestartSavesSessionBeforeRestartingServiceOnLinuxPaths(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewLinux(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	writeInstalledStateAndConfig(t, servicePaths, aria2c)
	events := []string{}
	serviceBackend := &recordingService{loaded: true, running: true, events: &events}
	rpc := &sessionRecordingRPC{events: &events, service: serviceBackend}
	application := newTestApp(servicePaths, aria2c, serviceBackend, rpc, app.Options{
		RPCReadyTimeout: time.Second,
		RPCPollInterval: time.Nanosecond,
	})

	if err := application.Restart(context.Background()); err != nil {
		t.Fatalf("restart: %v", err)
	}

	if rpc.saveSessionCalls != 1 {
		t.Fatalf("expected one saveSession call, got %d", rpc.saveSessionCalls)
	}
	if strings.Join(events, ",") != "saveSession,stop,start,version" {
		t.Fatalf("expected saveSession, stop, start, version poll, got %v", events)
	}
}

func TestStopCallsServiceStopWhenStateLoadFails(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	serviceBackend := &recordingService{loaded: true, running: true}
	// No state file written → state.Load will fail.
	rpc := &sessionRecordingRPC{}
	application := newTestApp(servicePaths, "", serviceBackend, rpc, app.Options{})

	err := application.Stop(context.Background())

	// State load failure should be reported.
	if err == nil {
		t.Fatal("expected stop to report state load error")
	}
	// Service.Stop() is always called, even when state is missing.
	if len(serviceBackend.calls) != 1 || serviceBackend.calls[0] != "stop" {
		t.Fatalf("expected service stop call, got %v", serviceBackend.calls)
	}
}

func TestRestartStopsAndRestartsWhenRPCUnavailable(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	writeInstalledStateAndConfig(t, servicePaths, aria2c)
	events := []string{}
	serviceBackend := &recordingService{
		loaded:            true,
		running:           false,
		events:            &events,
		shutdownLagChecks: 3,
	}
	rpc := &sessionRecordingRPC{
		service:        serviceBackend,
		events:         &events,
		saveSessionErr: fmt.Errorf("%w: dial tcp 127.0.0.1:6800: connect: connection refused", aria2.ErrTransportUnavailable),
	}
	application := newTestApp(servicePaths, aria2c, serviceBackend, rpc, app.Options{
		RPCReadyTimeout: time.Second,
		RPCPollInterval: time.Nanosecond,
	})

	if err := application.Restart(context.Background()); err != nil {
		t.Fatalf("restart should stop and restart: %v", err)
	}

	if rpc.shutdownCalls != 0 {
		t.Fatalf("expected no shutdown RPC, got %d", rpc.shutdownCalls)
	}
	if strings.Join(events, ",") != "saveSession,stop,start,version" {
		t.Fatalf("expected saveSession, stop, start, version, got %v", events)
	}
}

func TestInstallStartIgnoresExistingUserConfigChanges(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	current := writeInstalledStateAndConfig(t, servicePaths, aria2c)
	if err := aria2.WriteConfig(servicePaths.ConfigFile, "dir=/tmp/custom\nsplit=16\n"); err != nil {
		t.Fatalf("write custom config: %v", err)
	}
	plist, err := service.RenderLaunchAgent(current)
	if err != nil {
		t.Fatalf("render plist: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(servicePaths.ServiceFile), 0o755); err != nil {
		t.Fatalf("mkdir service dir: %v", err)
	}
	if err := os.WriteFile(servicePaths.ServiceFile, []byte(plist), 0o644); err != nil {
		t.Fatalf("write plist: %v", err)
	}
	if err := touch0600ForTest(servicePaths.SessionFile); err != nil {
		t.Fatalf("touch session: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(servicePaths.LogFile), 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	events := []string{}
	serviceBackend := &recordingService{loaded: true, running: true, events: &events}
	rpc := &sessionRecordingRPC{events: &events, service: serviceBackend}
	application := newTestApp(servicePaths, aria2c, serviceBackend, rpc, app.Options{
		RPCReadyTimeout: time.Second,
		RPCPollInterval: time.Nanosecond,
	})

	if err := application.Install(context.Background(), true); err != nil {
		t.Fatalf("install --start: %v", err)
	}

	if rpc.saveSessionCalls != 0 || rpc.shutdownCalls != 0 {
		t.Fatalf("expected no graceful restart, got save=%d shutdown=%d", rpc.saveSessionCalls, rpc.shutdownCalls)
	}
	if strings.Join(events, ",") != "version" {
		t.Fatalf("expected config changes to be ignored, got %v", events)
	}
}

func TestInstallWritesSystemdUnitForLinuxPaths(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	servicePaths := paths.Paths{
		ServiceName:  "aria2s.service",
		ServiceFile:  filepath.Join(home, ".config", "systemd", "user", "aria2s.service"),
		ConfigFile:   filepath.Join(home, ".aria2", "aria2.conf"),
		StateFile:    filepath.Join(home, ".local", "state", "aria2s", "state.json"),
		SessionFile:  filepath.Join(home, ".local", "state", "aria2s", "session"),
		LogFile:      filepath.Join(home, ".local", "state", "aria2s", "aria2.log"),
		ErrorLogFile: filepath.Join(home, ".local", "state", "aria2s", "aria2.err.log"),
	}
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	application := newTestApp(servicePaths, aria2c, &recordingService{}, fixedRPC{version: "1.37.0"}, app.Options{
		DownloadDir:   filepath.Join(root, "downloads"),
		RenderService: service.RenderSystemdUnit,
		IsPortAvailable: func(int) bool {
			return true
		},
	})

	if err := application.Install(context.Background(), false); err != nil {
		t.Fatalf("install: %v", err)
	}

	unit, err := os.ReadFile(servicePaths.ServiceFile)
	if err != nil {
		t.Fatalf("read service unit: %v", err)
	}

	text := string(unit)
	assertContains(t, text, "[Unit]")
	assertContains(t, text, "Description=aria2 RPC service managed by aria2s")
	assertContains(t, text, " managed-exec")
	assertContains(t, text, "WantedBy=default.target")
}

func TestInstallFailsOnCorruptStateWithoutOverwritingIt(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	if err := os.MkdirAll(filepath.Dir(servicePaths.StateFile), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(servicePaths.StateFile, []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("write corrupt state: %v", err)
	}
	application := newTestApp(servicePaths, aria2c, &recordingService{}, fixedRPC{version: "1.37.0"}, app.Options{})

	err := application.Install(context.Background(), false)

	if err == nil {
		t.Fatal("expected corrupt state to fail install")
	}
	if !strings.Contains(err.Error(), "state") {
		t.Fatalf("expected state error, got %v", err)
	}
	data, readErr := os.ReadFile(servicePaths.StateFile)
	if readErr != nil {
		t.Fatalf("read state: %v", readErr)
	}
	if string(data) != "{not-json" {
		t.Fatalf("expected corrupt state to remain untouched, got %q", data)
	}
}

func TestInstallReloadsLoadedServiceWhenPlistChanges(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	current := state.State{
		RuntimeSchemaVersion: 2,
		ControllerPath:       aria2c,
		StartupInputPath:     servicePaths.StartupInputFile,
		Aria2cPath:           aria2c,
		RPCPort:              6800,
		RPCSecret:            "secret-token",
		SessionPath:          servicePaths.SessionFile,
		LogPath:              servicePaths.LogFile,
		ErrorLogPath:         servicePaths.ErrorLogFile,
		ServiceName:          servicePaths.ServiceName,
	}
	if err := state.Save(servicePaths.StateFile, current); err != nil {
		t.Fatalf("save state: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(servicePaths.ServiceFile), 0o755); err != nil {
		t.Fatalf("mkdir service dir: %v", err)
	}
	if err := os.WriteFile(servicePaths.ServiceFile, []byte("stale plist"), 0o644); err != nil {
		t.Fatalf("write stale plist: %v", err)
	}
	serviceBackend := &recordingService{loaded: true}
	application := newTestApp(servicePaths, aria2c, serviceBackend, fixedRPC{version: "1.37.0"}, app.Options{})

	if err := application.Install(context.Background(), false); err != nil {
		t.Fatalf("install: %v", err)
	}

	wantCalls := []string{"uninstall", "install"}
	if strings.Join(serviceBackend.calls, ",") != strings.Join(wantCalls, ",") {
		t.Fatalf("expected reload calls %v, got %v", wantCalls, serviceBackend.calls)
	}
}

func TestInstallStartGracefullyStopsRunningServiceBeforeReloadingChangedPlist(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	current := state.State{
		RuntimeSchemaVersion: 2,
		ControllerPath:       aria2c,
		StartupInputPath:     servicePaths.StartupInputFile,
		Aria2cPath:           aria2c,
		RPCPort:              6800,
		RPCSecret:            "secret-token",
		SessionPath:          servicePaths.SessionFile,
		LogPath:              servicePaths.LogFile,
		ErrorLogPath:         servicePaths.ErrorLogFile,
		ServiceName:          servicePaths.ServiceName,
	}
	if err := state.Save(servicePaths.StateFile, current); err != nil {
		t.Fatalf("save state: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(servicePaths.ServiceFile), 0o755); err != nil {
		t.Fatalf("mkdir service dir: %v", err)
	}
	if err := os.WriteFile(servicePaths.ServiceFile, []byte("stale plist"), 0o644); err != nil {
		t.Fatalf("write stale plist: %v", err)
	}
	events := []string{}
	serviceBackend := &recordingService{loaded: true, running: true, events: &events}
	rpc := &sessionRecordingRPC{events: &events, service: serviceBackend}
	application := newTestApp(servicePaths, aria2c, serviceBackend, rpc, app.Options{
		RPCReadyTimeout: time.Second,
		RPCPollInterval: time.Nanosecond,
	})

	if err := application.Install(context.Background(), true); err != nil {
		t.Fatalf("install: %v", err)
	}

	if rpc.saveSessionCalls != 1 || rpc.shutdownCalls != 0 {
		t.Fatalf("expected one checkpoint and no shutdown, got save=%d shutdown=%d", rpc.saveSessionCalls, rpc.shutdownCalls)
	}
	if strings.Join(events, ",") != "saveSession,stop,uninstall,install,start,version" {
		t.Fatalf("expected checkpoint, stop, reinstall, start, version, got %v", events)
	}
}

func TestInstallPreservesRunningServiceAcrossChangedPlistWithoutStartFlag(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	current := state.State{
		RuntimeSchemaVersion: 2,
		ControllerPath:       aria2c,
		StartupInputPath:     servicePaths.StartupInputFile,
		Aria2cPath:           aria2c,
		RPCPort:              6800,
		RPCSecret:            "secret-token",
		SessionPath:          servicePaths.SessionFile,
		LogPath:              servicePaths.LogFile,
		ErrorLogPath:         servicePaths.ErrorLogFile,
		ServiceName:          servicePaths.ServiceName,
	}
	if err := state.Save(servicePaths.StateFile, current); err != nil {
		t.Fatalf("save state: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(servicePaths.ServiceFile), 0o755); err != nil {
		t.Fatalf("mkdir service dir: %v", err)
	}
	if err := os.WriteFile(servicePaths.ServiceFile, []byte("stale plist"), 0o644); err != nil {
		t.Fatalf("write stale plist: %v", err)
	}
	events := []string{}
	serviceBackend := &recordingService{loaded: true, running: true, events: &events}
	rpc := &sessionRecordingRPC{events: &events, service: serviceBackend}
	application := newTestApp(servicePaths, aria2c, serviceBackend, rpc, app.Options{})

	if err := application.Install(context.Background(), false); err != nil {
		t.Fatalf("install: %v", err)
	}

	if strings.Join(events, ",") != "saveSession,stop,uninstall,install,start" {
		t.Fatalf("expected checkpoint, stop, reinstall, start, got %v", events)
	}
}

func TestUninstallRemovesPlistWhenServiceAlreadyUnloaded(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	if err := os.MkdirAll(filepath.Dir(servicePaths.ServiceFile), 0o755); err != nil {
		t.Fatalf("mkdir service dir: %v", err)
	}
	if err := os.WriteFile(servicePaths.ServiceFile, []byte("plist"), 0o644); err != nil {
		t.Fatalf("write plist: %v", err)
	}
	application := newTestApp(servicePaths, aria2c, &unloadedService{}, fixedRPC{version: "1.37.0"}, app.Options{})

	if err := application.Uninstall(context.Background()); err != nil {
		t.Fatalf("uninstall should tolerate unloaded service: %v", err)
	}
	if _, err := os.Stat(servicePaths.ServiceFile); !os.IsNotExist(err) {
		t.Fatalf("expected service file removed, stat err: %v", err)
	}
}

func TestInstallWritesDefaultConfigWithoutBootstrappingWhenServiceAlreadyLoaded(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	current := writeInstalledStateAndConfig(t, servicePaths, aria2c)
	plist, err := service.RenderLaunchAgent(current)
	if err != nil {
		t.Fatalf("render plist: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(servicePaths.ServiceFile), 0o755); err != nil {
		t.Fatalf("mkdir service dir: %v", err)
	}
	if err := os.WriteFile(servicePaths.ServiceFile, []byte(plist), 0o644); err != nil {
		t.Fatalf("write plist: %v", err)
	}
	loadedService := &alreadyLoadedService{}
	application := newTestApp(servicePaths, aria2c, loadedService, fixedRPC{version: "1.37.0"}, app.Options{})

	if err := application.Install(context.Background(), false); err != nil {
		t.Fatalf("install should write default config without bootstrap: %v", err)
	}
	if loadedService.installCalls != 0 {
		t.Fatalf("expected no bootstrap for already loaded service, got %d calls", loadedService.installCalls)
	}

	config, err := os.ReadFile(servicePaths.ConfigFile)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	assertContains(t, string(config), "dir=")
	assertContains(t, string(config), "continue=true")
	assertNotContains(t, string(config), "rpc-secret")
}

func TestInstallLeavesExistingConfigUntouchedWhenAlreadyInstalled(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	current := writeInstalledStateAndConfig(t, servicePaths, aria2c)
	if err := aria2.WriteConfig(servicePaths.ConfigFile, "dir=/tmp/custom\nsplit=16\n"); err != nil {
		t.Fatalf("write existing config: %v", err)
	}
	if err := touch0600ForTest(servicePaths.SessionFile); err != nil {
		t.Fatalf("touch session: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(servicePaths.LogFile), 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	plist, err := service.RenderLaunchAgent(current)
	if err != nil {
		t.Fatalf("render plist: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(servicePaths.ServiceFile), 0o755); err != nil {
		t.Fatalf("mkdir service dir: %v", err)
	}
	if err := os.WriteFile(servicePaths.ServiceFile, []byte(plist), 0o644); err != nil {
		t.Fatalf("write plist: %v", err)
	}
	serviceBackend := &recordingService{loaded: true}
	application := newTestApp(servicePaths, aria2c, serviceBackend, fixedRPC{version: "1.37.0"}, app.Options{})

	stateStamp := fileModTime(t, servicePaths.StateFile)
	configStamp := fileModTime(t, servicePaths.ConfigFile)
	serviceStamp := fileModTime(t, servicePaths.ServiceFile)

	time.Sleep(10 * time.Millisecond)

	if err := application.Install(context.Background(), false); err != nil {
		t.Fatalf("install: %v", err)
	}
	if len(serviceBackend.calls) != 0 {
		t.Fatalf("expected install to short-circuit, got service calls %v", serviceBackend.calls)
	}
	if got := fileModTime(t, servicePaths.StateFile); !got.Equal(stateStamp) {
		t.Fatalf("expected state file to stay untouched, got %s want %s", got, stateStamp)
	}
	if got := fileModTime(t, servicePaths.ConfigFile); !got.Equal(configStamp) {
		t.Fatalf("expected config file to stay untouched, got %s want %s", got, configStamp)
	}
	if got := fileModTime(t, servicePaths.ServiceFile); !got.Equal(serviceStamp) {
		t.Fatalf("expected service file to stay untouched, got %s want %s", got, serviceStamp)
	}
}

func TestRebindManagedControllerRefreshesIdentityWithoutRestart(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	current := writeInstalledStateAndConfig(t, servicePaths, aria2c)
	current.ControllerIdentity = strings.Repeat("0", 64)
	if err := state.Save(servicePaths.StateFile, current); err != nil {
		t.Fatal(err)
	}
	serviceFile, err := service.RenderLaunchAgent(current)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(servicePaths.ServiceFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(servicePaths.ServiceFile, []byte(serviceFile), 0o644); err != nil {
		t.Fatal(err)
	}
	backend := &recordingService{loaded: true, running: true}
	application := newTestApp(servicePaths, aria2c, backend, fixedRPC{version: "1.37.0"}, app.Options{})

	bound, err := application.RebindManagedController(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !bound {
		t.Fatal("existing v2 controller was not rebound")
	}
	if len(backend.calls) != 0 || backend.inspectionCalls != 0 || !backend.running {
		t.Fatalf("running service was consulted or disturbed: inspections=%d calls=%v running=%v", backend.inspectionCalls, backend.calls, backend.running)
	}
	for _, path := range []string{servicePaths.ConfigFile, servicePaths.SessionFile, filepath.Dir(servicePaths.LogFile)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("controller-only rebind repaired unrelated path %s: %v", path, err)
		}
	}
	updated, err := state.Load(servicePaths.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	controllerData, err := os.ReadFile(updated.ControllerPath)
	if err != nil {
		t.Fatal(err)
	}
	wantIdentity := fmt.Sprintf("%x", sha256.Sum256(controllerData))
	want := current
	want.ControllerPath = updated.ControllerPath
	want.ControllerIdentity = wantIdentity
	if !reflect.DeepEqual(updated, want) {
		t.Fatalf("rebound state = %#v, want only controller identity changed from %#v", updated, current)
	}
}

func TestRebindManagedControllerReconcilesChangedServiceDefinition(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	current := writeInstalledStateAndConfig(t, servicePaths, aria2c)
	current.ControllerIdentity = strings.Repeat("0", 64)
	if err := state.Save(servicePaths.StateFile, current); err != nil {
		t.Fatal(err)
	}
	installedService, err := service.RenderLaunchAgent(current)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(servicePaths.ServiceFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(servicePaths.ServiceFile, []byte(installedService), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := touch0600ForTest(servicePaths.SessionFile); err != nil {
		t.Fatal(err)
	}
	backend := &recordingService{loaded: true, running: true}
	rpc := &sessionRecordingRPC{service: backend}
	application := newTestApp(servicePaths, aria2c, backend, rpc, app.Options{
		RenderService: func(current state.State) (string, error) {
			content, err := service.RenderLaunchAgent(current)
			if err != nil {
				return "", err
			}
			return content + "<!-- upgraded supervisor contract -->\n", nil
		},
	})

	bound, err := application.RebindManagedController(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !bound {
		t.Fatal("existing v2 controller was not rebound")
	}
	if got := strings.Join(backend.calls, ","); got != "stop,uninstall,install,start" {
		t.Fatalf("changed service reconciliation calls = %q", got)
	}
	if rpc.saveSessionCalls != 1 || !backend.running {
		t.Fatalf("service migration did not preserve runtime: saves=%d running=%v", rpc.saveSessionCalls, backend.running)
	}
	serviceData, err := os.ReadFile(servicePaths.ServiceFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(serviceData), "upgraded supervisor contract") {
		t.Fatalf("service artifact was not upgraded: %s", serviceData)
	}
	updated, err := state.Load(servicePaths.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	wantServiceIdentity := fmt.Sprintf("%x", sha256.Sum256(serviceData))
	if updated.ServiceIdentity != wantServiceIdentity {
		t.Fatalf("service identity = %q, want %q", updated.ServiceIdentity, wantServiceIdentity)
	}
}

func TestRebindManagedControllerLeavesUninstalledCLIAlone(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	backend := &recordingService{}
	application := newTestApp(servicePaths, aria2c, backend, fixedRPC{version: "1.37.0"}, app.Options{})
	bound, err := application.RebindManagedController(context.Background())
	if err != nil || bound {
		t.Fatalf("bound=%v err=%v", bound, err)
	}
	if len(backend.calls) != 0 {
		t.Fatalf("uninstalled CLI mutated service: %v", backend.calls)
	}
}

func writeExecutable(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir executable dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	return path
}

func writeInstalledStateAndConfig(t *testing.T, servicePaths paths.Paths, aria2c string) state.State {
	t.Helper()
	controller, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	controller, err = filepath.EvalSymlinks(controller)
	if err != nil {
		t.Fatal(err)
	}
	controllerData, err := os.ReadFile(controller)
	if err != nil {
		t.Fatal(err)
	}
	controllerHash := sha256.Sum256(controllerData)
	current := state.State{
		RuntimeSchemaVersion: 2,
		ControllerPath:       controller,
		ControllerIdentity:   hex.EncodeToString(controllerHash[:]),
		StartupInputPath:     servicePaths.StartupInputFile,
		Aria2cPath:           aria2c,
		RPCPort:              6800,
		RPCSecret:            "secret-token",
		SessionPath:          servicePaths.SessionFile,
		LogPath:              servicePaths.LogFile,
		ErrorLogPath:         servicePaths.ErrorLogFile,
		ServiceName:          servicePaths.ServiceName,
	}
	serviceFile, err := service.RenderLaunchAgent(current)
	if err != nil {
		t.Fatal(err)
	}
	serviceHash := sha256.Sum256([]byte(serviceFile))
	current.ServiceIdentity = hex.EncodeToString(serviceHash[:])
	if err := state.Save(servicePaths.StateFile, current); err != nil {
		t.Fatalf("save state: %v", err)
	}
	return current
}

func touch0600ForTest(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func fileModTime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.ModTime()
}

func newTestApp(servicePaths paths.Paths, aria2c string, serviceBackend service.Backend, rpc app.RPC, overrides app.Options) *app.App {
	options := overrides
	options.Paths = servicePaths
	options.LookPath = func(string) (string, error) {
		return aria2c, nil
	}
	options.Abs = func(path string) (string, error) {
		return path, nil
	}
	if options.IsPortAvailable == nil {
		options.IsPortAvailable = func(int) bool {
			return false
		}
	}
	options.GenerateSecret = func() (string, error) {
		return "secret-token", nil
	}
	options.Service = serviceBackend
	options.RPC = rpc
	return app.New(options)
}

type recordingService struct {
	loaded            bool
	running           bool
	calls             []string
	events            *[]string
	inspectionCalls   int
	shutdownLagChecks int
}

type recoveryService struct {
	loaded     bool
	running    bool
	restarted  bool
	marker     string
	markerSeen bool
	calls      []string
}

func (service *recoveryService) Install(context.Context) error { return nil }
func (service *recoveryService) Uninstall(context.Context) error {
	service.loaded, service.running = false, false
	return nil
}
func (service *recoveryService) Start(context.Context) error {
	service.calls = append(service.calls, "start")
	service.loaded, service.running, service.restarted = true, true, true
	_, err := os.Stat(service.marker)
	service.markerSeen = err == nil
	return err
}
func (service *recoveryService) Stop(context.Context) error {
	service.calls = append(service.calls, "stop")
	service.loaded, service.running = false, false
	return nil
}
func (service *recoveryService) IsLoaded(context.Context) bool  { return service.loaded }
func (service *recoveryService) IsRunning(context.Context) bool { return service.running }

type recoveryRPC struct{ service *recoveryService }

func (rpc *recoveryRPC) Version(context.Context, state.State) (string, error) {
	if !rpc.service.restarted {
		return "", errors.New("RPC blocked")
	}
	return "1.37.0", nil
}
func (*recoveryRPC) AddURI(context.Context, state.State, string, aria2.AddOptions) (string, error) {
	return "", nil
}
func (*recoveryRPC) SaveSession(context.Context, state.State) error { return nil }
func (*recoveryRPC) Shutdown(context.Context, state.State) error    { return nil }

type alwaysUnavailableRPC struct{}

func (alwaysUnavailableRPC) Version(context.Context, state.State) (string, error) {
	return "", errors.New("RPC blocked")
}
func (alwaysUnavailableRPC) AddURI(context.Context, state.State, string, aria2.AddOptions) (string, error) {
	return "", nil
}
func (alwaysUnavailableRPC) SaveSession(context.Context, state.State) error { return nil }
func (alwaysUnavailableRPC) Shutdown(context.Context, state.State) error    { return nil }

func (service *recordingService) Install(context.Context) error {
	service.calls = append(service.calls, "install")
	if service.events != nil {
		*service.events = append(*service.events, "install")
	}
	service.loaded = true
	return nil
}

func (service *recordingService) Uninstall(context.Context) error {
	service.calls = append(service.calls, "uninstall")
	if service.events != nil {
		*service.events = append(*service.events, "uninstall")
	}
	service.loaded = false
	service.running = false
	return nil
}

func (service *recordingService) Start(context.Context) error {
	service.calls = append(service.calls, "start")
	if service.events != nil {
		*service.events = append(*service.events, "start")
	}
	service.loaded = true
	service.running = true
	return nil
}

func (service *recordingService) Stop(context.Context) error {
	service.calls = append(service.calls, "stop")
	if service.events != nil {
		*service.events = append(*service.events, "stop")
	}
	service.running = false
	return nil
}

func (service *recordingService) IsLoaded(context.Context) bool {
	service.inspectionCalls++
	return service.loaded
}

func (service *recordingService) IsRunning(context.Context) bool {
	service.inspectionCalls++
	if !service.running && service.shutdownLagChecks > 0 {
		service.shutdownLagChecks--
		return true
	}
	return service.running
}

type unloadedService struct{}

func (service *unloadedService) Install(context.Context) error {
	return nil
}

func (service *unloadedService) Uninstall(context.Context) error {
	return errors.New("service is not loaded")
}

func (service *unloadedService) Start(context.Context) error {
	return nil
}

func (service *unloadedService) Stop(context.Context) error {
	return nil
}

func (service *unloadedService) IsLoaded(context.Context) bool {
	return false
}

func (service *unloadedService) IsRunning(context.Context) bool {
	return false
}

type alreadyLoadedService struct {
	installCalls int
}

func (service *alreadyLoadedService) Install(context.Context) error {
	service.installCalls++
	return errors.New("bootstrap failed because service is already loaded")
}

func (service *alreadyLoadedService) Uninstall(context.Context) error {
	return nil
}

func (service *alreadyLoadedService) Start(context.Context) error {
	return nil
}

func (service *alreadyLoadedService) Stop(context.Context) error {
	return nil
}

func (service *alreadyLoadedService) IsLoaded(context.Context) bool {
	return true
}

func (service *alreadyLoadedService) IsRunning(context.Context) bool {
	return false
}

type fixedRPC struct {
	version string
}

func (rpc fixedRPC) Version(context.Context, state.State) (string, error) {
	return rpc.version, nil
}

func (rpc fixedRPC) AddURI(context.Context, state.State, string, aria2.AddOptions) (string, error) {
	return "2089b05ecca3d829", nil
}

func (rpc fixedRPC) SaveSession(context.Context, state.State) error {
	return nil
}

func (rpc fixedRPC) Shutdown(context.Context, state.State) error {
	return nil
}

type flakyRPC struct {
	failuresRemaining int
	version           string
	versionCalls      int
}

func (rpc *flakyRPC) Version(context.Context, state.State) (string, error) {
	rpc.versionCalls++
	if rpc.failuresRemaining > 0 {
		rpc.failuresRemaining--
		return "", errors.New("connection refused")
	}
	return rpc.version, nil
}

func (rpc *flakyRPC) AddURI(context.Context, state.State, string, aria2.AddOptions) (string, error) {
	return "2089b05ecca3d829", nil
}

func (rpc *flakyRPC) SaveSession(context.Context, state.State) error {
	return nil
}

func (rpc *flakyRPC) Shutdown(context.Context, state.State) error {
	return nil
}

func (*flakyRPC) ReadBatch(context.Context, state.State, aria2.ReadBatchQuery) (aria2.ReadBatch, error) {
	return aria2.ReadBatch{}, nil
}
func (*flakyRPC) TaskDetail(context.Context, state.State, string) (aria2.DownloadDetail, error) {
	return aria2.DownloadDetail{}, nil
}
func (*flakyRPC) Pause(context.Context, state.State, string) error  { return nil }
func (*flakyRPC) Resume(context.Context, state.State, string) error { return nil }
func (*flakyRPC) RetrySource(context.Context, state.State, string) (aria2.RetrySource, error) {
	return aria2.RetrySource{}, nil
}
func (*flakyRPC) AddURIs(context.Context, state.State, []string, aria2.AddOptions) (string, error) {
	return "", nil
}
func (*flakyRPC) Remove(context.Context, state.State, string) error       { return nil }
func (*flakyRPC) ClearStopped(context.Context, state.State, string) error { return nil }

type dirRecordingRPC struct {
	lastDir string
}

func (rpc *dirRecordingRPC) Version(context.Context, state.State) (string, error) {
	return "1.37.0", nil
}

func (rpc *dirRecordingRPC) AddURI(_ context.Context, _ state.State, _ string, opts aria2.AddOptions) (string, error) {
	rpc.lastDir = opts.Dir
	return "2089b05ecca3d829", nil
}

func (rpc *dirRecordingRPC) SaveSession(context.Context, state.State) error {
	return nil
}

func (rpc *dirRecordingRPC) Shutdown(context.Context, state.State) error {
	return nil
}

type sessionRecordingRPC struct {
	saveSessionCalls int
	shutdownCalls    int
	saveSessionErr   error
	shutdownErr      error
	events           *[]string
	service          *recordingService
}

func (rpc *sessionRecordingRPC) CompleteCensus(context.Context, state.State) ([]aria2.LifecycleStatus, error) {
	return nil, nil
}

func (rpc *sessionRecordingRPC) Version(context.Context, state.State) (string, error) {
	if rpc.service != nil && !rpc.service.running {
		return "", errors.New("connection refused")
	}
	if rpc.events != nil {
		*rpc.events = append(*rpc.events, "version")
	}
	return "1.37.0", nil
}

func (rpc *sessionRecordingRPC) AddURI(context.Context, state.State, string, aria2.AddOptions) (string, error) {
	return "2089b05ecca3d829", nil
}

func (rpc *sessionRecordingRPC) SaveSession(context.Context, state.State) error {
	rpc.saveSessionCalls++
	if rpc.events != nil {
		*rpc.events = append(*rpc.events, "saveSession")
	}
	return rpc.saveSessionErr
}

func (rpc *sessionRecordingRPC) Shutdown(context.Context, state.State) error {
	rpc.shutdownCalls++
	if rpc.events != nil {
		*rpc.events = append(*rpc.events, "shutdown")
	}
	if rpc.shutdownErr != nil {
		return rpc.shutdownErr
	}
	if rpc.service != nil {
		rpc.service.running = false
	}
	return nil
}

func TestAddRecordsCustomDirAndExposesRecentDirs(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	rpc := &dirRecordingRPC{}
	application := newTestApp(servicePaths, aria2c, &recordingService{}, rpc, app.Options{
		DownloadDir: filepath.Join(root, "Downloads"),
	})
	writeInstalledStateAndConfig(t, servicePaths, aria2c)

	if _, err := application.Add(context.Background(), "https://example.com/a.zip", aria2.AddOptions{Dir: "/data/Movies"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if rpc.lastDir != "/data/Movies" {
		t.Fatalf("rpc received dir %q, want /data/Movies", rpc.lastDir)
	}

	recent, err := application.RecentDirs(context.Background())
	if err != nil {
		t.Fatalf("recent dirs: %v", err)
	}
	if len(recent) != 1 || recent[0] != "/data/Movies" {
		t.Fatalf("recent dirs got %#v, want [/data/Movies]", recent)
	}

	// Adding the same dir again should dedup, not duplicate.
	if _, err := application.Add(context.Background(), "https://example.com/b.zip", aria2.AddOptions{Dir: "/data/Movies"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	recent, _ = application.RecentDirs(context.Background())
	if len(recent) != 1 {
		t.Fatalf("expected deduped single recent dir, got %#v", recent)
	}

	// A new dir is recorded at the front.
	if _, err := application.Add(context.Background(), "https://example.com/c.zip", aria2.AddOptions{Dir: "/data/Music"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	recent, _ = application.RecentDirs(context.Background())
	if len(recent) != 2 || recent[0] != "/data/Music" || recent[1] != "/data/Movies" {
		t.Fatalf("expected [Music Movies], got %#v", recent)
	}
}

func TestAddWithoutDirDoesNotRecord(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	rpc := &dirRecordingRPC{}
	application := newTestApp(servicePaths, aria2c, &recordingService{}, rpc, app.Options{})
	writeInstalledStateAndConfig(t, servicePaths, aria2c)

	if _, err := application.Add(context.Background(), "https://example.com/a.zip", aria2.AddOptions{}); err != nil {
		t.Fatalf("add: %v", err)
	}
	recent, err := application.RecentDirs(context.Background())
	if err != nil {
		t.Fatalf("recent dirs: %v", err)
	}
	if len(recent) != 0 {
		t.Fatalf("expected no recent dirs when dir unset, got %#v", recent)
	}
}

func assertContains(t *testing.T, text, want string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Fatalf("expected %q to contain %q", text, want)
	}
}

func assertNotContains(t *testing.T, text, want string) {
	t.Helper()
	if strings.Contains(text, want) {
		t.Fatalf("expected %q not to contain %q", text, want)
	}
}
