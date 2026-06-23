package doctor_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amio/aria2s/cmd"
	"github.com/amio/aria2s/internal/app"
	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/doctor"
	"github.com/amio/aria2s/internal/paths"
	"github.com/amio/aria2s/internal/service"
	"github.com/amio/aria2s/internal/state"
)

func TestCheckDetectsMissingBinaryPortConflictAndManagedConfigDrift(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	current := state.State{
		Aria2cPath:   filepath.Join(root, "missing-aria2c"),
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
		t.Fatalf("write config: %v", err)
	}

	report := doctor.Check(context.Background(), doctor.Options{
		Paths: servicePaths,
		IsPortAvailable: func(port int) bool {
			return port != 6800
		},
	})

	if report.Healthy {
		t.Fatal("expected unhealthy report")
	}
	assertReportContains(t, report, "missing aria2c binary")
	assertReportContains(t, report, "port conflict")
	assertReportContains(t, report, "managed config drift")
}

func TestCheckDoesNotReportPortConflictWhenManagedRPCIsReachable(t *testing.T) {
	root := t.TempDir()
	aria2c := filepath.Join(root, "aria2c")
	if err := os.WriteFile(aria2c, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write aria2c: %v", err)
	}
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
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
		DownloadDir: filepath.Join(root, "Downloads"),
	}, nil)); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(servicePaths.ServiceFile), 0o755); err != nil {
		t.Fatalf("mkdir service dir: %v", err)
	}
	if err := os.WriteFile(servicePaths.ServiceFile, []byte("plist"), 0o644); err != nil {
		t.Fatalf("write service file: %v", err)
	}

	report := doctor.Check(context.Background(), doctor.Options{
		Paths: servicePaths,
		IsPortAvailable: func(int) bool {
			return false
		},
		Service: fixedService{
			loaded:  true,
			running: true,
		},
		RPCReachable: func(context.Context, state.State) bool {
			return true
		},
	})

	if !report.Healthy {
		t.Fatalf("expected healthy report, got %#v", report.Issues)
	}
}

func TestCheckReportsSupervisorDrift(t *testing.T) {
	root := t.TempDir()
	aria2c := filepath.Join(root, "aria2c")
	if err := os.WriteFile(aria2c, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write aria2c: %v", err)
	}
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
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
		DownloadDir: filepath.Join(root, "Downloads"),
	}, nil)); err != nil {
		t.Fatalf("write config: %v", err)
	}

	report := doctor.Check(context.Background(), doctor.Options{
		Paths: servicePaths,
		Service: fixedService{
			loaded:  false,
			running: false,
		},
		IsPortAvailable: func(int) bool {
			return true
		},
	})

	if report.Healthy {
		t.Fatal("expected supervisor drift report")
	}
	assertReportContains(t, report, "missing service file")
	assertReportContains(t, report, "LaunchAgent unloaded")
}

func TestCheckReportsNotRunningAndRPCUnreachable(t *testing.T) {
	root := t.TempDir()
	aria2c := filepath.Join(root, "aria2c")
	if err := os.WriteFile(aria2c, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write aria2c: %v", err)
	}
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
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
		DownloadDir: filepath.Join(root, "Downloads"),
	}, nil)); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(servicePaths.ServiceFile), 0o755); err != nil {
		t.Fatalf("mkdir service dir: %v", err)
	}
	if err := os.WriteFile(servicePaths.ServiceFile, []byte("plist"), 0o644); err != nil {
		t.Fatalf("write service file: %v", err)
	}

	report := doctor.Check(context.Background(), doctor.Options{
		Paths: servicePaths,
		Service: fixedService{
			loaded:  true,
			running: false,
		},
		IsPortAvailable: func(int) bool {
			return true
		},
		RPCReachable: func(context.Context, state.State) bool {
			return false
		},
	})

	if report.Healthy {
		t.Fatal("expected unhealthy report")
	}
	assertReportContains(t, report, "LaunchAgent not running")
	assertReportContains(t, report, "RPC unreachable")
}

func TestStatusReportOmitsRPCSecret(t *testing.T) {
	root := t.TempDir()
	aria2c := filepath.Join(root, "aria2c")
	if err := os.WriteFile(aria2c, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write aria2c: %v", err)
	}
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
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
		RPCPort:     6800,
		RPCSecret:   "secret-token",
		SessionFile: servicePaths.SessionFile,
		DownloadDir: filepath.Join(root, "Downloads"),
	}, nil)); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(servicePaths.ServiceFile), 0o755); err != nil {
		t.Fatalf("mkdir service dir: %v", err)
	}
	if err := os.WriteFile(servicePaths.ServiceFile, []byte("plist"), 0o600); err != nil {
		t.Fatalf("write service file: %v", err)
	}

	report := doctor.Status(context.Background(), doctor.StatusOptions{
		Paths: servicePaths,
		Service: fixedService{
			loaded:  true,
			running: true,
		},
		RPCVersion: func(context.Context, state.State) (string, error) {
			return "1.37.0", nil
		},
	})
	output := report.String()

	assertContains(t, output, "Service:    installed")
	assertContains(t, output, "Supervisor: running")
	assertContains(t, output, "Binary:     valid")
	assertContains(t, output, "RPC:        reachable")
	assertContains(t, output, "aria2:      1.37.0")
	assertContains(t, output, "Endpoint:   http://127.0.0.1:6800/jsonrpc")
	if strings.Contains(output, "secret-token") {
		t.Fatalf("status leaked RPC secret: %s", output)
	}
}

func TestInstallStartStatusAndAddCommands(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	servicePaths := paths.NewDarwin(home)
	aria2c := filepath.Join(root, "bin", "aria2c")
	if err := os.MkdirAll(filepath.Dir(aria2c), 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := os.WriteFile(aria2c, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write aria2c: %v", err)
	}
	rpc := &fakeRPC{version: "1.37.0", gid: "2089b05ecca3d829"}
	serviceBackend := &fakeService{}
	application := app.New(app.Options{
		Paths: servicePaths,
		LookPath: func(name string) (string, error) {
			if name == "aria2c" {
				return aria2c, nil
			}
			return "", os.ErrNotExist
		},
		Abs: func(path string) (string, error) {
			return filepath.Abs(path)
		},
		IsPortAvailable: func(port int) bool {
			return port == 6800
		},
		GenerateSecret: func() (string, error) {
			return "secret-token", nil
		},
		Service: serviceBackend,
		RPC:     rpc,
	})

	installOut, installErr := runCommand(t, application, "install", "--start")
	if installErr != nil {
		t.Fatalf("install failed: %v", installErr)
	}
	assertContains(t, installOut, "aria2s installed and started.")
	if !serviceBackend.started {
		t.Fatal("expected install --start to start service")
	}

	statusOut, statusErr := runCommand(t, application, "status")
	if statusErr != nil {
		t.Fatalf("status failed: %v", statusErr)
	}
	assertContains(t, statusOut, "RPC:        reachable")
	assertContains(t, statusOut, "Endpoint:   http://127.0.0.1:6800/jsonrpc")
	if strings.Contains(statusOut, "secret-token") {
		t.Fatalf("status leaked RPC secret: %s", statusOut)
	}

	addOut, addErr := runCommand(t, application, "add", "https://example.com/file.zip")
	if addErr != nil {
		t.Fatalf("add failed: %v", addErr)
	}
	assertContains(t, addOut, "Added download.")
	assertContains(t, addOut, "2089b05ecca3d829")
	if rpc.addedURI != "https://example.com/file.zip" {
		t.Fatalf("unexpected added URI: %s", rpc.addedURI)
	}

	writtenState, err := state.Load(servicePaths.StateFile)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if writtenState.RPCPort != 6800 || writtenState.RPCSecret != "secret-token" {
		t.Fatalf("unexpected state: %#v", writtenState)
	}
	if _, err := os.Stat(servicePaths.ServiceFile); err != nil {
		t.Fatalf("expected service file: %v", err)
	}
}

func TestLogsCommandPrintsRecentLogContent(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	if err := os.MkdirAll(filepath.Dir(servicePaths.LogFile), 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	if err := os.WriteFile(servicePaths.LogFile, []byte("stdout line 1\nstdout line 2\n"), 0o644); err != nil {
		t.Fatalf("write stdout log: %v", err)
	}
	if err := os.WriteFile(servicePaths.ErrorLogFile, []byte("stderr line 1\nstderr line 2\n"), 0o644); err != nil {
		t.Fatalf("write stderr log: %v", err)
	}
	application := app.New(app.Options{Paths: servicePaths})

	output, err := runCommand(t, application, "logs")
	if err != nil {
		t.Fatalf("logs: %v", err)
	}

	assertContains(t, output, "stdout line 2")
	assertContains(t, output, "stderr line 2")
}

type fixedService struct {
	loaded  bool
	running bool
}

type fakeService struct {
	installed bool
	started   bool
}

func (service *fakeService) Install(context.Context) error {
	service.installed = true
	return nil
}

func (service *fakeService) Uninstall(context.Context) error {
	service.installed = false
	service.started = false
	return nil
}

func (service *fakeService) Start(context.Context) error {
	service.started = true
	service.installed = true
	return nil
}

func (service *fakeService) Stop(context.Context) error {
	service.started = false
	return nil
}

func (service *fakeService) Restart(ctx context.Context) error {
	if err := service.Stop(ctx); err != nil {
		return err
	}
	return service.Start(ctx)
}

func (service *fakeService) IsLoaded(context.Context) bool {
	return service.installed
}

func (service *fakeService) IsRunning(context.Context) bool {
	return service.started
}

var _ service.Backend = (*fakeService)(nil)

type fakeRPC struct {
	version  string
	gid      string
	addedURI string
}

func (rpc *fakeRPC) Version(context.Context, state.State) (string, error) {
	return rpc.version, nil
}

func (rpc *fakeRPC) AddURI(_ context.Context, _ state.State, uri string, _ aria2.AddOptions) (string, error) {
	rpc.addedURI = uri
	return rpc.gid, nil
}

func (rpc *fakeRPC) SaveSession(context.Context, state.State) error {
	return nil
}

func (rpc *fakeRPC) Shutdown(context.Context, state.State) error {
	return nil
}

func runCommand(t *testing.T, application *app.App, args ...string) (string, error) {
	t.Helper()
	var stdout bytes.Buffer
	root := cmd.NewRoot(application)
	root.SetOut(&stdout)
	root.SetErr(&stdout)
	root.SetArgs(args)
	err := root.Execute()
	return stdout.String(), err
}

func (service fixedService) IsLoaded(context.Context) bool {
	return service.loaded
}

func (service fixedService) IsRunning(context.Context) bool {
	return service.running
}

func assertReportContains(t *testing.T, report doctor.Report, want string) {
	t.Helper()
	for _, issue := range report.Issues {
		if strings.Contains(issue.Message, want) {
			return
		}
	}
	t.Fatalf("expected report to contain %q, got %#v", want, report.Issues)
}

func assertContains(t *testing.T, text, want string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Fatalf("expected %q to contain %q", text, want)
	}
}
