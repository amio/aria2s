package app

import (
	"path/filepath"
	"testing"

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
