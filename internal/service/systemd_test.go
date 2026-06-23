package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/amio/aria2s/internal/service"
	"github.com/amio/aria2s/internal/state"
)

func TestRenderSystemdUnitUsesAbsoluteAria2cPathWithoutShell(t *testing.T) {
	current := state.State{
		Aria2cPath:   "/usr/bin/aria2c",
		ConfigPath:   "/home/amio/.config/aria2s/aria2.conf",
		LogPath:      "/home/amio/.local/state/aria2s/aria2.log",
		ErrorLogPath: "/home/amio/.local/state/aria2s/aria2.err.log",
	}

	rendered, err := service.RenderSystemdUnit(current)
	if err != nil {
		t.Fatalf("render systemd unit: %v", err)
	}

	assertContains(t, rendered, "[Unit]")
	assertContains(t, rendered, "Description=aria2 RPC service managed by aria2s")
	assertContains(t, rendered, "ExecStart=/usr/bin/aria2c --conf-path=/home/amio/.config/aria2s/aria2.conf")
	assertContains(t, rendered, "StandardOutput=append:/home/amio/.local/state/aria2s/aria2.log")
	assertContains(t, rendered, "StandardError=append:/home/amio/.local/state/aria2s/aria2.err.log")
	assertContains(t, rendered, "[Install]")
	assertContains(t, rendered, "WantedBy=default.target")
	assertNotContains(t, rendered, "/bin/sh")
	assertNotContains(t, rendered, " -c ")
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
