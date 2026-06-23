package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/amio/aria2s/internal/service"
	"github.com/amio/aria2s/internal/state"
)

func TestRenderLaunchAgentUsesAbsoluteAria2cPathWithoutShell(t *testing.T) {
	current := state.State{
		Aria2cPath:   "/opt/homebrew/bin/aria2c",
		ConfigPath:   "/Users/amio/Library/Application Support/aria2s/aria2.conf",
		LogPath:      "/Users/amio/Library/Logs/aria2s/aria2.log",
		ErrorLogPath: "/Users/amio/Library/Logs/aria2s/aria2.err.log",
		ServiceName:  "io.github.amio.aria2s",
	}

	rendered, err := service.RenderLaunchAgent(current)
	if err != nil {
		t.Fatalf("render launch agent: %v", err)
	}

	assertContains(t, rendered, "<key>ProgramArguments</key>")
	assertContains(t, rendered, "<string>/opt/homebrew/bin/aria2c</string>")
	assertContains(t, rendered, "<string>--conf-path=/Users/amio/Library/Application Support/aria2s/aria2.conf</string>")
	assertContains(t, rendered, "<key>RunAtLoad</key>")
	assertContains(t, rendered, "<false/>")
	assertContains(t, rendered, "<key>KeepAlive</key>")
	assertContains(t, rendered, "<key>SuccessfulExit</key>")
	assertNotContains(t, rendered, "<key>KeepAlive</key>\n  <true/>")
	assertNotContains(t, rendered, "<key>KeepAlive</key>\n  <false/>")
	assertNotContains(t, rendered, "<string>sh</string>")
	assertNotContains(t, rendered, "<string>-c</string>")
}

func TestLaunchdBackendGeneratesBootstrapCommands(t *testing.T) {
	runner := &printAwareRunner{loaded: false}
	backend := service.NewLaunchdBackend(runner, 501, "io.github.amio.aria2s", "/tmp/io.github.amio.aria2s.plist")

	if err := backend.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := backend.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := backend.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}

	want := []string{
		"launchctl print gui/501/io.github.amio.aria2s",
		"launchctl bootstrap gui/501 /tmp/io.github.amio.aria2s.plist",
		"launchctl print gui/501/io.github.amio.aria2s",
		"launchctl print gui/501/io.github.amio.aria2s",
		"launchctl kickstart gui/501/io.github.amio.aria2s",
		"launchctl print gui/501/io.github.amio.aria2s",
		"launchctl kill SIGTERM gui/501/io.github.amio.aria2s",
	}
	if strings.Join(runner.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unexpected commands:\n%s", strings.Join(runner.calls, "\n"))
	}
}

func TestLaunchdStopKeepsServiceLoaded(t *testing.T) {
	runner := &printAwareRunner{loaded: true, running: true}
	backend := service.NewLaunchdBackend(runner, 501, "io.github.amio.aria2s", "/tmp/io.github.amio.aria2s.plist")

	if err := backend.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !runner.loaded {
		t.Fatal("expected stop to keep the service loaded")
	}
}

func TestLaunchdStartBootstrapsBeforeKickstartWhenUnloaded(t *testing.T) {
	runner := &printAwareRunner{loaded: false}
	backend := service.NewLaunchdBackend(runner, 501, "io.github.amio.aria2s", "/tmp/io.github.amio.aria2s.plist")

	if err := backend.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	want := []string{
		"launchctl print gui/501/io.github.amio.aria2s",
		"launchctl print gui/501/io.github.amio.aria2s",
		"launchctl bootstrap gui/501 /tmp/io.github.amio.aria2s.plist",
		"launchctl kickstart gui/501/io.github.amio.aria2s",
	}
	if strings.Join(runner.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unexpected commands:\n%s", strings.Join(runner.calls, "\n"))
	}
}

func TestLaunchdStartDoesNothingWhenAlreadyRunning(t *testing.T) {
	runner := &printAwareRunner{loaded: true, running: true}
	backend := service.NewLaunchdBackend(runner, 501, "io.github.amio.aria2s", "/tmp/io.github.amio.aria2s.plist")

	if err := backend.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	want := []string{"launchctl print gui/501/io.github.amio.aria2s"}
	if strings.Join(runner.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unexpected commands:\n%s", strings.Join(runner.calls, "\n"))
	}
}

func TestLaunchdInstallSkipsBootstrapWhenAlreadyLoaded(t *testing.T) {
	runner := &printAwareRunner{loaded: true}
	backend := service.NewLaunchdBackend(runner, 501, "io.github.amio.aria2s", "/tmp/io.github.amio.aria2s.plist")

	if err := backend.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}

	want := []string{"launchctl print gui/501/io.github.amio.aria2s"}
	if strings.Join(runner.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unexpected commands:\n%s", strings.Join(runner.calls, "\n"))
	}
}

func TestLaunchdUninstallIgnoresAlreadyUnloaded(t *testing.T) {
	runner := &printAwareRunner{loaded: false}
	backend := service.NewLaunchdBackend(runner, 501, "io.github.amio.aria2s", "/tmp/io.github.amio.aria2s.plist")

	if err := backend.Uninstall(context.Background()); err != nil {
		t.Fatalf("uninstall should ignore unloaded service: %v", err)
	}

	want := []string{"launchctl print gui/501/io.github.amio.aria2s"}
	if strings.Join(runner.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unexpected commands:\n%s", strings.Join(runner.calls, "\n"))
	}
}

func TestLaunchdStopIgnoresAlreadyUnloaded(t *testing.T) {
	runner := &printAwareRunner{loaded: false}
	backend := service.NewLaunchdBackend(runner, 501, "io.github.amio.aria2s", "/tmp/io.github.amio.aria2s.plist")

	if err := backend.Stop(context.Background()); err != nil {
		t.Fatalf("stop should ignore unloaded service: %v", err)
	}

	want := []string{"launchctl print gui/501/io.github.amio.aria2s"}
	if strings.Join(runner.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unexpected commands:\n%s", strings.Join(runner.calls, "\n"))
	}
}

type printAwareRunner struct {
	loaded  bool
	running bool
	calls   []string
}

func (runner *printAwareRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := name + " " + strings.Join(args, " ")
	runner.calls = append(runner.calls, call)
	if call == "launchctl print gui/501/io.github.amio.aria2s" && !runner.loaded {
		return nil, errLaunchdNotLoaded{}
	}
	if call == "launchctl print gui/501/io.github.amio.aria2s" && runner.running {
		return []byte("state = running\npid = 123\n"), nil
	}
	if call == "launchctl bootstrap gui/501 /tmp/io.github.amio.aria2s.plist" {
		runner.loaded = true
	}
	if call == "launchctl kill SIGTERM gui/501/io.github.amio.aria2s" {
		runner.running = false
	}
	if call == "launchctl bootout gui/501/io.github.amio.aria2s" {
		runner.loaded = false
		runner.running = false
	}
	return nil, nil
}

type errLaunchdNotLoaded struct{}

func (errLaunchdNotLoaded) Error() string {
	return "service not loaded"
}

func assertContains(t *testing.T, text, want string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Fatalf("expected %q to contain %q", text, want)
	}
}

func assertNotContains(t *testing.T, text, want string) {
	t.Helper()
	if strings.Contains(text, want) {
		t.Fatalf("expected %q not to contain %q", text, want)
	}
}
