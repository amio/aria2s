package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/amio/aria2s/internal/app"
	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/paths"
	"github.com/amio/aria2s/internal/service"
	"github.com/amio/aria2s/internal/state"
)

func TestResolveVersionUsesGoInstallBuildMetadata(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}
	if got := resolveVersion("dev", info, true); got != "v1.2.3" {
		t.Fatalf("resolved version = %q", got)
	}
	if got := resolveVersion("1.2.4", info, true); got != "1.2.4" {
		t.Fatalf("ldflags version = %q", got)
	}
	if got := resolveVersion("dev", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true); got != "dev" {
		t.Fatalf("development version = %q", got)
	}
}

func TestVersionFlagAndCommand(t *testing.T) {
	application := newTestApp(paths.NewDarwin(t.TempDir()), "", &recordingService{}, &trackingRPC{})
	for _, args := range [][]string{{"-v"}, {"--version"}, {"version"}} {
		root := newRoot(application, nil)
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatalf("execute %v: %v", args, err)
		}
		got := strings.TrimSpace(out.String())
		if !strings.Contains(got, currentVersion()) {
			t.Fatalf("args %v: expected version %q in output %q", args, currentVersion(), got)
		}
	}
}

func TestRootWithoutArgsOpensDashboard(t *testing.T) {
	rootDir := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(rootDir, "home"))
	aria2c := writeExecutable(t, filepath.Join(rootDir, "bin", "aria2c"))
	current := writeInstalledStateAndConfig(t, servicePaths, aria2c)
	if err := os.MkdirAll(filepath.Dir(servicePaths.LogFile), 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	if err := touch0600ForTest(servicePaths.SessionFile); err != nil {
		t.Fatalf("touch session: %v", err)
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
	rpc := &trackingRPC{version: "1.37.0"}
	application := newTestApp(servicePaths, aria2c, serviceBackend, rpc)

	calls := 0
	runner := func(context.Context, *app.DashboardSession) error {
		calls++
		return nil
	}

	root := newRoot(application, runner)
	root.SetArgs(nil)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute root: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected root command to open dashboard once, got %d", calls)
	}
	if len(serviceBackend.calls) != 0 {
		t.Fatalf("expected ready dashboard launch to skip service calls, got %v", serviceBackend.calls)
	}
	if rpc.versionCalls != 0 {
		t.Fatalf("dashboard preparation must not probe RPC, got %d", rpc.versionCalls)
	}
}

func TestRootWithoutArgsUsesStoredInstallWhenLookPathFails(t *testing.T) {
	rootDir := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(rootDir, "home"))
	aria2c := writeExecutable(t, filepath.Join(rootDir, "bin", "aria2c"))
	current := writeInstalledStateAndConfig(t, servicePaths, aria2c)
	if err := os.MkdirAll(filepath.Dir(servicePaths.LogFile), 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	if err := touch0600ForTest(servicePaths.SessionFile); err != nil {
		t.Fatalf("touch session: %v", err)
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
	rpc := &trackingRPC{version: "1.37.0"}
	application := app.New(app.Options{
		Paths: servicePaths,
		LookPath: func(string) (string, error) {
			return "", os.ErrNotExist
		},
		Abs: func(path string) (string, error) {
			return path, nil
		},
		GenerateSecret: func() (string, error) {
			return "secret-token", nil
		},
		IsPortAvailable: func(int) bool {
			return true
		},
		Service:         serviceBackend,
		RPC:             rpc,
		RPCReadyTimeout: time.Second,
		RPCPollInterval: time.Nanosecond,
	})

	calls := 0
	runner := func(context.Context, *app.DashboardSession) error {
		calls++
		return nil
	}

	root := newRoot(application, runner)
	root.SetArgs(nil)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute root: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected root command to open dashboard once, got %d", calls)
	}
	if rpc.versionCalls != 0 {
		t.Fatalf("dashboard preparation must not probe RPC, got %d", rpc.versionCalls)
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
	controller, err := os.ReadFile(aria2c)
	if err != nil {
		t.Fatalf("read controller: %v", err)
	}
	controllerSum := sha256.Sum256(controller)
	current.ControllerIdentity = hex.EncodeToString(controllerSum[:])
	serviceFile, err := service.RenderLaunchAgent(current)
	if err != nil {
		t.Fatalf("render service: %v", err)
	}
	serviceSum := sha256.Sum256([]byte(serviceFile))
	current.ServiceIdentity = hex.EncodeToString(serviceSum[:])
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

func newTestApp(servicePaths paths.Paths, aria2c string, serviceBackend service.Backend, rpc app.RPC) *app.App {
	return app.New(app.Options{
		Paths: servicePaths,
		LookPath: func(string) (string, error) {
			return aria2c, nil
		},
		Abs: func(path string) (string, error) {
			return path, nil
		},
		GenerateSecret: func() (string, error) {
			return "secret-token", nil
		},
		IsPortAvailable: func(int) bool {
			return true
		},
		Service:         serviceBackend,
		RPC:             rpc,
		RPCReadyTimeout: time.Second,
		RPCPollInterval: time.Nanosecond,
	})
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

func (service *recordingService) IsLoaded(context.Context) bool {
	return service.loaded
}

func (service *recordingService) IsRunning(context.Context) bool {
	return service.running
}

type trackingRPC struct {
	version      string
	versionCalls int
}

func (rpc *trackingRPC) Version(context.Context, state.State) (string, error) {
	rpc.versionCalls++
	return rpc.version, nil
}

func (rpc *trackingRPC) AddURI(context.Context, state.State, string, aria2.AddOptions) (string, error) {
	return "2089b05ecca3d829", nil
}

func (rpc *trackingRPC) SaveSession(context.Context, state.State) error {
	return nil
}

func (*trackingRPC) CompleteCensus(context.Context, state.State) ([]aria2.LifecycleStatus, error) {
	return nil, nil
}

func (rpc *trackingRPC) Shutdown(context.Context, state.State) error {
	return nil
}

func (*trackingRPC) DashboardSnapshot(context.Context, state.State, aria2.DashboardQuery) (aria2.DashboardRead, error) {
	return aria2.DashboardRead{}, nil
}
func (*trackingRPC) TaskDetail(context.Context, state.State, string) (aria2.DownloadDetail, error) {
	return aria2.DownloadDetail{}, nil
}
func (*trackingRPC) Pause(context.Context, state.State, string) error  { return nil }
func (*trackingRPC) Resume(context.Context, state.State, string) error { return nil }
func (*trackingRPC) RetrySource(context.Context, state.State, string) (aria2.RetrySource, error) {
	return aria2.RetrySource{}, nil
}
func (*trackingRPC) AddURIs(context.Context, state.State, []string, aria2.AddOptions) (string, error) {
	return "", nil
}
func (*trackingRPC) Remove(context.Context, state.State, string) error       { return nil }
func (*trackingRPC) ClearStopped(context.Context, state.State, string) error { return nil }
