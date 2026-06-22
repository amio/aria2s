package app_test

import (
	"context"
	"errors"
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
	assertContains(t, message, "asv doctor")
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

func TestRestartFailsWhenManagedConfigDrifted(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	writeInstalledStateAndConfig(t, servicePaths, aria2c)
	if err := aria2.WriteConfig(servicePaths.ConfigFile, "rpc-listen-port=9999\nrpc-secret=wrong\n"); err != nil {
		t.Fatalf("write drifted config: %v", err)
	}
	serviceBackend := &recordingService{}
	application := newTestApp(servicePaths, aria2c, serviceBackend, fixedRPC{version: "1.37.0"}, app.Options{})

	err := application.Restart(context.Background())

	if err == nil {
		t.Fatal("expected config drift to fail restart")
	}
	assertContains(t, err.Error(), "managed config drift")
	assertContains(t, err.Error(), "asv install")
	if len(serviceBackend.calls) != 0 {
		t.Fatalf("expected no service calls, got %v", serviceBackend.calls)
	}
}

func TestInstallStartRestartsRunningServiceWhenManagedConfigChanges(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	current := writeInstalledStateAndConfig(t, servicePaths, aria2c)
	if err := aria2.WriteConfig(servicePaths.ConfigFile, "rpc-listen-port=9999\nrpc-secret=wrong\n"); err != nil {
		t.Fatalf("write drifted config: %v", err)
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
	application := newTestApp(servicePaths, aria2c, serviceBackend, fixedRPC{version: "1.37.0"}, app.Options{})

	if err := application.Install(context.Background(), true); err != nil {
		t.Fatalf("install --start: %v", err)
	}

	if strings.Join(serviceBackend.calls, ",") != "restart" {
		t.Fatalf("expected restart after config repair, got %v", serviceBackend.calls)
	}
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
		ConfigPath:   servicePaths.ConfigFile,
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

func TestInstallRepairsFilesWithoutBootstrappingWhenServiceAlreadyLoaded(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	aria2c := writeExecutable(t, filepath.Join(root, "bin", "aria2c"))
	current := state.State{
		Aria2cPath:   aria2c,
		RPCPort:      6800,
		RPCSecret:    "secret-token",
		ConfigPath:   servicePaths.ConfigFile,
		SessionPath:  servicePaths.SessionFile,
		LogPath:      servicePaths.LogFile,
		ErrorLogPath: servicePaths.ErrorLogFile,
		ServiceName:  servicePaths.ServiceName,
	}
	if err := state.Save(servicePaths.StateFile, current); err != nil {
		t.Fatalf("save state: %v", err)
	}
	if err := aria2.WriteConfig(servicePaths.ConfigFile, "rpc-listen-port=9999\nrpc-secret=wrong\n"); err != nil {
		t.Fatalf("write drifted config: %v", err)
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
		t.Fatalf("install should repair loaded service without bootstrap: %v", err)
	}
	if loadedService.installCalls != 0 {
		t.Fatalf("expected no bootstrap for already loaded service, got %d calls", loadedService.installCalls)
	}

	values, err := aria2.ReadConfig(servicePaths.ConfigFile)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if aria2.HasManagedDrift(values, current) {
		t.Fatal("expected install to repair managed config drift")
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
		ConfigPath:   servicePaths.ConfigFile,
		SessionPath:  servicePaths.SessionFile,
		LogPath:      servicePaths.LogFile,
		ErrorLogPath: servicePaths.ErrorLogFile,
		ServiceName:  servicePaths.ServiceName,
	}
	if err := state.Save(servicePaths.StateFile, current); err != nil {
		t.Fatalf("save state: %v", err)
	}
	if err := aria2.WriteConfig(servicePaths.ConfigFile, aria2.BuildConfig(aria2.ManagedConfig{
		RPCPort:     current.RPCPort,
		RPCSecret:   current.RPCSecret,
		SessionFile: current.SessionPath,
		DownloadDir: filepath.Join(filepath.Dir(servicePaths.ConfigFile), "downloads"),
	}, nil)); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return current
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
	loaded  bool
	running bool
	calls   []string
}

func (service *recordingService) Install(context.Context) error {
	service.calls = append(service.calls, "install")
	service.loaded = true
	return nil
}

func (service *recordingService) Uninstall(context.Context) error {
	service.calls = append(service.calls, "uninstall")
	service.loaded = false
	service.running = false
	return nil
}

func (service *recordingService) Start(context.Context) error {
	service.calls = append(service.calls, "start")
	service.loaded = true
	service.running = true
	return nil
}

func (service *recordingService) Stop(context.Context) error {
	service.calls = append(service.calls, "stop")
	service.running = false
	return nil
}

func (service *recordingService) Restart(context.Context) error {
	service.calls = append(service.calls, "restart")
	service.loaded = true
	service.running = true
	return nil
}

func (service *recordingService) IsLoaded(context.Context) bool {
	return service.loaded
}

func (service *recordingService) IsRunning(context.Context) bool {
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

func (service *unloadedService) Restart(context.Context) error {
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

func (service *alreadyLoadedService) Restart(context.Context) error {
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
