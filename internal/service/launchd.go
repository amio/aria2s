package service

import (
	"context"
	"encoding/xml"
	"fmt"
	"os/exec"
	"strings"

	"github.com/amio/aria2s/internal/state"
)

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type LaunchdBackend struct {
	runner    CommandRunner
	uid       int
	label     string
	plistPath string
}

func NewLaunchdBackend(runner CommandRunner, uid int, label, plistPath string) *LaunchdBackend {
	return &LaunchdBackend{runner: runner, uid: uid, label: label, plistPath: plistPath}
}

func (backend *LaunchdBackend) Install(ctx context.Context) error {
	if backend.IsLoaded(ctx) {
		return nil
	}
	return backend.bootstrap(ctx)
}

func (backend *LaunchdBackend) bootstrap(ctx context.Context) error {
	_, err := backend.runner.Run(ctx, "launchctl", "bootstrap", backend.domain(), backend.plistPath)
	return err
}

func (backend *LaunchdBackend) Uninstall(ctx context.Context) error {
	if !backend.IsLoaded(ctx) {
		return nil
	}
	_, err := backend.runner.Run(ctx, "launchctl", "bootout", backend.domain(), backend.plistPath)
	return err
}

func (backend *LaunchdBackend) Start(ctx context.Context) error {
	if backend.IsRunning(ctx) {
		return nil
	}
	if !backend.IsLoaded(ctx) {
		if err := backend.bootstrap(ctx); err != nil {
			return err
		}
	}
	_, err := backend.runner.Run(ctx, "launchctl", "kickstart", backend.serviceTarget())
	return err
}

func (backend *LaunchdBackend) Stop(ctx context.Context) error {
	if !backend.IsLoaded(ctx) {
		return nil
	}
	_, err := backend.runner.Run(ctx, "launchctl", "kill", "SIGTERM", backend.serviceTarget())
	return err
}

func (backend *LaunchdBackend) IsLoaded(ctx context.Context) bool {
	_, err := backend.runner.Run(ctx, "launchctl", "print", backend.serviceTarget())
	return err == nil
}

func (backend *LaunchdBackend) IsRunning(ctx context.Context) bool {
	output, err := backend.runner.Run(ctx, "launchctl", "print", backend.serviceTarget())
	if err != nil {
		return false
	}
	text := string(output)
	return strings.Contains(text, "state = running") || strings.Contains(text, "pid =")
}

func (backend *LaunchdBackend) domain() string {
	return fmt.Sprintf("gui/%d", backend.uid)
}

func (backend *LaunchdBackend) serviceTarget() string {
	return backend.domain() + "/" + backend.label
}

func RenderLaunchAgent(current state.State) (string, error) {
	if current.Aria2cPath == "" {
		return "", fmt.Errorf("aria2c path is required")
	}
	var builder strings.Builder
	builder.WriteString(xml.Header)
	builder.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">`)
	builder.WriteString("\n<plist version=\"1.0\">\n<dict>\n")
	writePlistString(&builder, "Label", current.ServiceName)
	builder.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	writePlistArrayString(&builder, current.Aria2cPath)
	writePlistArrayString(&builder, "--conf-path="+current.ConfigPath)
	builder.WriteString("  </array>\n")
	builder.WriteString("  <key>RunAtLoad</key>\n  <false/>\n")
	builder.WriteString("  <key>KeepAlive</key>\n  <dict>\n")
	builder.WriteString("    <key>SuccessfulExit</key>\n    <false/>\n")
	builder.WriteString("  </dict>\n")
	writePlistString(&builder, "StandardOutPath", current.LogPath)
	writePlistString(&builder, "StandardErrorPath", current.ErrorLogPath)
	builder.WriteString("</dict>\n</plist>\n")
	return builder.String(), nil
}

func writePlistString(builder *strings.Builder, key, value string) {
	builder.WriteString("  <key>")
	xml.EscapeText(builder, []byte(key))
	builder.WriteString("</key>\n  <string>")
	xml.EscapeText(builder, []byte(value))
	builder.WriteString("</string>\n")
}

func writePlistArrayString(builder *strings.Builder, value string) {
	builder.WriteString("    <string>")
	xml.EscapeText(builder, []byte(value))
	builder.WriteString("</string>\n")
}
