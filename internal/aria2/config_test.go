package aria2_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amio/aria2s/internal/aria2"
)

func TestBuildManagedConfigRepairsManagedKeysButPreservesUserKeys(t *testing.T) {
	managed := aria2.ManagedConfig{
		RPCPort:     6800,
		RPCSecret:   "secret-token",
		SessionFile: "/tmp/session",
		DownloadDir: "/tmp/downloads",
	}
	current := map[string]string{
		"dir":                   "/Users/amio/Downloads/custom",
		"split":                 "16",
		"rpc-listen-port":       "9999",
		"save-session-interval": "10",
	}

	rendered := aria2.BuildConfig(managed, current)

	assertContains(t, rendered, "enable-rpc=true")
	assertContains(t, rendered, "rpc-listen-all=false")
	assertContains(t, rendered, "rpc-listen-port=6800")
	assertContains(t, rendered, "rpc-secret=secret-token")
	assertContains(t, rendered, "input-file=/tmp/session")
	assertContains(t, rendered, "save-session=/tmp/session")
	assertContains(t, rendered, "save-session-interval=60")
	assertContains(t, rendered, "dir=/Users/amio/Downloads/custom")
	assertContains(t, rendered, "split=16")
}

func TestWriteConfigWrites0600(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "aria2s", "aria2.conf")

	if err := aria2.WriteConfig(configFile, "enable-rpc=true\n"); err != nil {
		t.Fatalf("write config: %v", err)
	}

	info, err := os.Stat(configFile)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("expected 0600, got %o", got)
	}
}

func assertContains(t *testing.T, text, want string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Fatalf("expected %q to contain %q", text, want)
	}
}
