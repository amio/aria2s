package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/amio/aria2s/internal/app"
	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/paths"
	"github.com/amio/aria2s/internal/service"
	"github.com/amio/aria2s/internal/state"
)

func TestRootWithoutArgsOpensConsole(t *testing.T) {
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
	application.SetConsoleRunner(func(*app.App) error {
		calls++
		return nil
	})

	root := NewRoot(application)
	root.SetArgs(nil)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute root: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected root command to open console once, got %d", calls)
	}
	if len(serviceBackend.calls) != 0 {
		t.Fatalf("expected ready console launch to skip service calls, got %v", serviceBackend.calls)
	}
	if rpc.versionCalls != 1 {
		t.Fatalf("expected one readiness probe, got %d", rpc.versionCalls)
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
	application.SetConsoleRunner(func(*app.App) error {
		calls++
		return nil
	})

	root := NewRoot(application)
	root.SetArgs(nil)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute root: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected root command to open console once, got %d", calls)
	}
	if rpc.versionCalls != 1 {
		t.Fatalf("expected one readiness probe, got %d", rpc.versionCalls)
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

func (rpc *trackingRPC) Shutdown(context.Context, state.State) error {
	return nil
}
