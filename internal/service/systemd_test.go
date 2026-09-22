package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/amio/aria2s/internal/service"
	"github.com/amio/aria2s/internal/state"
)

func TestRenderSystemdUnitUsesManagedControllerWithoutShell(t *testing.T) {
	current := state.State{
		RuntimeSchemaVersion: 2,
		ControllerPath:       "/usr/local/bin/aria2s",
		Aria2cPath:           "/usr/bin/aria2c",
		RPCPort:              6800,
		RPCSecret:            "secret-token",
		SessionPath:          "/home/amio/.local/state/aria2s/session",
		LogPath:              "/home/amio/.local/state/aria2s/aria2.log",
		ErrorLogPath:         "/home/amio/.local/state/aria2s/aria2.err.log",
	}

	rendered, err := service.RenderSystemdUnit(current)
	if err != nil {
		t.Fatalf("render systemd unit: %v", err)
	}

	assertContains(t, rendered, "[Unit]")
	assertContains(t, rendered, "Description=aria2 RPC service managed by aria2s")
	assertContains(t, rendered, "ExecStart=/usr/local/bin/aria2s managed-exec")
	assertContains(t, rendered, "StandardOutput=null")
	assertContains(t, rendered, "StandardError=null")
	assertNotContains(t, rendered, current.Aria2cPath)
	assertContains(t, rendered, "LimitNOFILE=65536")
	assertContains(t, rendered, "[Install]")
	assertContains(t, rendered, "WantedBy=default.target")
	assertNotContains(t, rendered, "/bin/sh")
	assertNotContains(t, rendered, " -c ")
}

func TestRenderSystemdUnitEscapesRuntimeV2ControllerPath(t *testing.T) {
	current := state.State{
		RuntimeSchemaVersion: 2,
		Aria2cPath:           "/opt/aria 2/aria2c",
		ControllerPath:       "/opt/aria 2/aria2s$prod%name",
		LogPath:              "/home/amio/.local/state/aria2s/aria2.log",
		ErrorLogPath:         "/home/amio/.local/state/aria2s/aria2.err.log",
	}
	rendered, err := service.RenderSystemdUnit(current)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, rendered, `ExecStart="/opt/aria 2/aria2s$$prod%%name" managed-exec`)
	assertContains(t, rendered, "StandardOutput=null")
	assertContains(t, rendered, "StandardError=null")
	assertNotContains(t, rendered, current.LogPath)
	assertNotContains(t, rendered, current.ErrorLogPath)
}

func TestServiceRenderersRejectUnsupportedRuntimeSchemas(t *testing.T) {
	for name, render := range map[string]func(state.State) (string, error){
		"launchd": service.RenderLaunchAgent,
		"systemd": service.RenderSystemdUnit,
	} {
		t.Run(name, func(t *testing.T) {
			for _, version := range []int{0, 1, 3} {
				current := state.State{RuntimeSchemaVersion: version, Aria2cPath: "/usr/bin/aria2c", ControllerPath: "/usr/local/bin/aria2s"}
				if _, err := render(current); err == nil {
					t.Fatalf("rendered unsupported runtime schema %d", version)
				}
			}
		})
	}
}

func TestSystemdBackendGeneratesLifecycleCommands(t *testing.T) {
	runner := &systemdAwareRunner{loaded: false}
	backend := service.NewSystemdBackend(runner, "aria2s.service")

	if err := backend.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := backend.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := backend.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := backend.Uninstall(context.Background()); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user is-enabled aria2s.service",
		"systemctl --user enable aria2s.service",
		"systemctl --user is-active --quiet aria2s.service",
		"systemctl --user is-enabled aria2s.service",
		"systemctl --user start aria2s.service",
		"systemctl --user is-enabled aria2s.service",
		"systemctl --user stop aria2s.service",
		"systemctl --user is-enabled aria2s.service",
		"systemctl --user disable --now aria2s.service",
		"systemctl --user daemon-reload",
	}
	if strings.Join(runner.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unexpected commands:\n%s", strings.Join(runner.calls, "\n"))
	}
}

func TestSystemdStartDoesNothingWhenAlreadyRunning(t *testing.T) {
	runner := &systemdAwareRunner{loaded: true, running: true}
	backend := service.NewSystemdBackend(runner, "aria2s.service")

	if err := backend.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	want := []string{"systemctl --user is-active --quiet aria2s.service"}
	if strings.Join(runner.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unexpected commands:\n%s", strings.Join(runner.calls, "\n"))
	}
}

func TestSystemdInstallWrapsUnavailableUserSessionErrors(t *testing.T) {
	runner := &failingSystemdRunner{
		output: "Failed to connect to bus: No medium found",
		err:    errors.New("exit status 1"),
	}
	backend := service.NewSystemdBackend(runner, "aria2s.service")

	err := backend.Install(context.Background())
	if err == nil {
		t.Fatal("expected install to fail")
	}
	assertContains(t, err.Error(), "systemd --user")
	assertContains(t, err.Error(), "live user session")
	assertContains(t, err.Error(), "Failed to connect to bus: No medium found")
}

type systemdAwareRunner struct {
	loaded  bool
	running bool
	calls   []string
}

func (runner *systemdAwareRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := name + " " + strings.Join(args, " ")
	runner.calls = append(runner.calls, call)
	switch call {
	case "systemctl --user is-enabled aria2s.service":
		if !runner.loaded {
			return nil, errSystemdDisabled{}
		}
	case "systemctl --user is-active --quiet aria2s.service":
		if !runner.running {
			return nil, errSystemdInactive{}
		}
	case "systemctl --user enable aria2s.service":
		runner.loaded = true
	case "systemctl --user start aria2s.service":
		runner.loaded = true
		runner.running = true
	case "systemctl --user stop aria2s.service":
		runner.running = false
	case "systemctl --user disable --now aria2s.service":
		runner.loaded = false
		runner.running = false
	}
	return nil, nil
}

type errSystemdDisabled struct{}

func (errSystemdDisabled) Error() string {
	return "disabled"
}

type errSystemdInactive struct{}

func (errSystemdInactive) Error() string {
	return "inactive"
}

type failingSystemdRunner struct {
	output string
	err    error
	calls  []string
}

func (runner *failingSystemdRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := name + " " + strings.Join(args, " ")
	runner.calls = append(runner.calls, call)
	return []byte(runner.output), runner.err
}
