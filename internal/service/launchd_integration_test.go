//go:build darwin

package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLaunchdLifecycleIntegration(t *testing.T) {
	if os.Getenv("ARIA2S_LAUNCHD_INTEGRATION") != "1" {
		t.Skip("set ARIA2S_LAUNCHD_INTEGRATION=1 to exercise the real user launchd domain")
	}
	root := t.TempDir()
	label := fmt.Sprintf("io.github.amio.aria2s.integration.%d", os.Getpid())
	script := filepath.Join(root, "service.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nwhile :; do sleep 1; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(root, label+".plist")
	content := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array><string>%s</string></array>
  <key>RunAtLoad</key><false/>
</dict>
</plist>
`, label, script)
	if err := os.WriteFile(plist, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := NewLaunchdBackend(ExecRunner{}, os.Getuid(), label, plist)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer backend.Uninstall(context.Background())
	if err := backend.Install(ctx); err != nil {
		t.Fatalf("bootstrap isolated LaunchAgent: %v", err)
	}
	if !backend.IsLoaded(ctx) {
		t.Fatal("isolated LaunchAgent was not loaded")
	}
	if err := backend.Start(ctx); err != nil {
		t.Fatalf("kickstart isolated LaunchAgent: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !backend.IsRunning(ctx) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !backend.IsRunning(ctx) {
		t.Fatal("isolated LaunchAgent did not enter running state")
	}
	if err := backend.Stop(ctx); err != nil {
		t.Fatalf("bootout isolated LaunchAgent: %v", err)
	}
	if backend.IsLoaded(ctx) {
		t.Fatal("isolated LaunchAgent remained loaded after bootout")
	}
}
