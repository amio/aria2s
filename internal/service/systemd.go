package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/state"
)

type SystemdBackend struct {
	runner   CommandRunner
	unitName string
}

func NewSystemdBackend(runner CommandRunner, unitName string) *SystemdBackend {
	return &SystemdBackend{runner: runner, unitName: unitName}
}

func (backend *SystemdBackend) Install(ctx context.Context) error {
	if err := backend.reload(ctx); err != nil {
		return err
	}
	if backend.IsLoaded(ctx) {
		return nil
	}
	_, err := backend.run(ctx, "enable", backend.unitName)
	return err
}

func (backend *SystemdBackend) Uninstall(ctx context.Context) error {
	if !backend.IsLoaded(ctx) {
		return nil
	}
	if _, err := backend.run(ctx, "disable", "--now", backend.unitName); err != nil {
		return err
	}
	return backend.reload(ctx)
}

func (backend *SystemdBackend) Start(ctx context.Context) error {
	if backend.IsRunning(ctx) {
		return nil
	}
	if !backend.IsLoaded(ctx) {
		if err := backend.Install(ctx); err != nil {
			return err
		}
	}
	_, err := backend.run(ctx, "start", backend.unitName)
	return err
}

func (backend *SystemdBackend) Stop(ctx context.Context) error {
	if !backend.IsLoaded(ctx) {
		return nil
	}
	_, err := backend.run(ctx, "stop", backend.unitName)
	return err
}

func (backend *SystemdBackend) IsLoaded(ctx context.Context) bool {
	_, err := backend.runner.Run(ctx, "systemctl", "--user", "is-enabled", backend.unitName)
	return err == nil
}

func (backend *SystemdBackend) IsRunning(ctx context.Context) bool {
	_, err := backend.runner.Run(ctx, "systemctl", "--user", "is-active", "--quiet", backend.unitName)
	return err == nil
}

func (backend *SystemdBackend) reload(ctx context.Context) error {
	_, err := backend.run(ctx, "daemon-reload")
	return err
}

func (backend *SystemdBackend) run(ctx context.Context, args ...string) ([]byte, error) {
	fullArgs := append([]string{"--user"}, args...)
	output, err := backend.runner.Run(ctx, "systemctl", fullArgs...)
	if err == nil {
		return output, nil
	}
	command := "systemctl " + strings.Join(fullArgs, " ")
	message := strings.TrimSpace(string(output))
	guidance := "systemd --user requires a live user session and is often unavailable in containers, WSL, or minimal Linux installs"
	if message != "" {
		return nil, fmt.Errorf("%s failed: %s. %s: %w", command, message, guidance, err)
	}
	return nil, fmt.Errorf("%s failed. %s: %w", command, guidance, err)
}

func RenderSystemdUnit(current state.State) (string, error) {
	if current.Aria2cPath == "" {
		return "", fmt.Errorf("aria2c path is required")
	}
	var builder strings.Builder
	builder.WriteString("[Unit]\n")
	builder.WriteString("Description=aria2 RPC service managed by aria2s\n")
	builder.WriteString("After=network-online.target\n\n")
	builder.WriteString("[Service]\n")
	builder.WriteString("Type=simple\n")
	builder.WriteString("ExecStart=")
	builder.WriteString(current.Aria2cPath)
	for _, arg := range aria2.ManagedArgs(current) {
		builder.WriteByte(' ')
		builder.WriteString(arg)
	}
	builder.WriteString("\n")
	builder.WriteString("Restart=on-failure\n")
	builder.WriteString("RestartSec=3\n")
	if current.LogPath != "" {
		builder.WriteString("StandardOutput=append:")
		builder.WriteString(current.LogPath)
		builder.WriteString("\n")
	}
	if current.ErrorLogPath != "" {
		builder.WriteString("StandardError=append:")
		builder.WriteString(current.ErrorLogPath)
		builder.WriteString("\n")
	}
	builder.WriteString("\n[Install]\n")
	builder.WriteString("WantedBy=default.target\n")
	return builder.String(), nil
}
