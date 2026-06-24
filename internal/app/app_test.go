package app_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

	if err := application.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	if strings.Join(serviceBackend.calls, ",") != "start" {
		t.Fatalf("expected start call, got %v", serviceBackend.calls)
	}
	if rpc.versionCalls != 2 {
		t.Fatalf("expected RPC readiness polling, got %d calls", rpc.versionCalls)
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

func TestEnsureDashboardReadyIgnoresMissingUserConfig(t *testing.T) {
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

	if err := application.EnsureDashboardReady(context.Background()); err != nil {
		t.Fatalf("ensure dashboard ready: %v", err)
	}
	if len(serviceBackend.calls) != 0 {
		t.Fatalf("expected no service calls, got %v", serviceBackend.calls)
	}
	if rpc.versionCalls != 1 {
		t.Fatalf("expected one readiness probe, got %d", rpc.versionCalls)
	}
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
	assertContains(t, text, "ExecStart="+aria2c+" --enable-rpc=true --rpc-listen-all=false --rpc-listen-port=6800 --rpc-secret=secret-token --input-file="+servicePaths.SessionFile+" --save-session="+servicePaths.SessionFile+" --force-save=true --save-session-interval=60")
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
		Aria2cPath:   aria2c,
		RPCPort:      6800,
		RPCSecret:    "secret-token",
		SessionPath:  servicePaths.SessionFile,
		LogPath:      servicePaths.LogFile,
		ErrorLogPath: servicePaths.ErrorLogFile,
		ServiceName:  servicePaths.ServiceName,
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
		Aria2cPath:   aria2c,
		RPCPort:      6800,
		RPCSecret:    "secret-token",
		SessionPath:  servicePaths.SessionFile,
		LogPath:      servicePaths.LogFile,
		ErrorLogPath: servicePaths.ErrorLogFile,
		ServiceName:  servicePaths.ServiceName,
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

	if rpc.saveSessionCalls != 0 || rpc.shutdownCalls != 0 {
		t.Fatalf("expected no saveSession or shutdown, got save=%d shutdown=%d", rpc.saveSessionCalls, rpc.shutdownCalls)
	}
	if strings.Join(events, ",") != "stop,uninstall,install,start,version" {
		t.Fatalf("expected stop, uninstall, install, start, version, got %v", events)
	}
}

func TestInstallPreservesRunningServiceAcrossChangedPlistWithoutStartFlag(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	current := state.State{
		Aria2cPath:   aria2c,
		RPCPort:      6800,
		RPCSecret:    "secret-token",
		SessionPath:  servicePaths.SessionFile,
		LogPath:      servicePaths.LogFile,
		ErrorLogPath: servicePaths.ErrorLogFile,
		ServiceName:  servicePaths.ServiceName,
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

	if strings.Join(events, ",") != "stop,uninstall,install,start" {
		t.Fatalf("expected stop, uninstall, install, start, got %v", events)
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
	current := state.State{
		Aria2cPath:   aria2c,
		RPCPort:      6800,
		RPCSecret:    "secret-token",
		SessionPath:  servicePaths.SessionFile,
		LogPath:      servicePaths.LogFile,
		ErrorLogPath: servicePaths.ErrorLogFile,
		ServiceName:  servicePaths.ServiceName,
	}
	if err := state.Save(servicePaths.StateFile, current); err != nil {
		t.Fatalf("save state: %v", err)
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
	current := state.State{
		Aria2cPath:   aria2c,
		RPCPort:      6800,
		RPCSecret:    "secret-token",
		SessionPath:  servicePaths.SessionFile,
		LogPath:      servicePaths.LogFile,
		ErrorLogPath: servicePaths.ErrorLogFile,
		ServiceName:  servicePaths.ServiceName,
	}
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
	shutdownLagChecks int
}

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
	return service.loaded
}

func (service *recordingService) IsRunning(context.Context) bool {
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
