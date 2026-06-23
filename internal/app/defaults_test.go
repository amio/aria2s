package app

import (
	"path/filepath"
	"testing"

	"github.com/amio/aria2s/internal/paths"
	"github.com/amio/aria2s/internal/service"
	"github.com/amio/aria2s/internal/state"
)

func TestDefaultOptionsForLinuxUseSystemdPathsAndRenderer(t *testing.T) {
	root := t.TempDir()

	options, err := defaultOptionsForOS("linux", filepath.Join(root, "home"), 501, service.ExecRunner{})
	if err != nil {
		t.Fatalf("default options for linux: %v", err)
	}

	if options.Paths.ServiceName != "aria2s.service" {
		t.Fatalf("unexpected linux service name: %s", options.Paths.ServiceName)
	}
	unit, err := options.RenderService(state.State{
		Aria2cPath:   "/usr/bin/aria2c",
		ConfigPath:   options.Paths.ConfigFile,
		LogPath:      options.Paths.LogFile,
		ErrorLogPath: options.Paths.ErrorLogFile,
	})
	if err != nil {
		t.Fatalf("render linux service: %v", err)
	}
	if unit == "" {
		t.Fatal("expected rendered systemd unit")
	}
}

func TestNewInfersLinuxServiceDefaultsFromLinuxPaths(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewLinux(filepath.Join(root, "home"))

	application := New(Options{Paths: servicePaths})

	if application.options.Service == nil {
		t.Fatal("expected app.New to infer a service backend for Linux paths")
	}
	unit, err := application.options.RenderService(state.State{
		Aria2cPath:   "/usr/bin/aria2c",
		ConfigPath:   servicePaths.ConfigFile,
		LogPath:      servicePaths.LogFile,
		ErrorLogPath: servicePaths.ErrorLogFile,
	})
	if err != nil {
		t.Fatalf("render inferred linux service: %v", err)
	}
	if unit == "" || unit[0] != '[' {
		t.Fatalf("expected systemd unit output, got %q", unit)
	}
}
