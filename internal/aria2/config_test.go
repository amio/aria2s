package aria2_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/state"
)

func TestDefaultConfigIncludesFriendlyDefaults(t *testing.T) {
	rendered := aria2.DefaultConfig("/tmp/downloads")

	assertContains(t, rendered, "dir=/tmp/downloads")
	assertContains(t, rendered, "continue=true")
	assertContains(t, rendered, "max-concurrent-downloads=5")
	assertContains(t, rendered, "split=8")
	assertContains(t, rendered, "max-connection-per-server=8")
	assertContains(t, rendered, "min-split-size=10M")
	assertNotContains(t, rendered, "rpc-listen-port")
	assertNotContains(t, rendered, "rpc-secret")
	assertNotContains(t, rendered, "save-session")
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

func TestManagedArgsIncludeRPCAndSessionFlags(t *testing.T) {
	current := state.State{
		RPCPort:     6800,
		RPCSecret:   "secret-token",
		SessionPath: "/tmp/session",
	}

	args := aria2.ManagedArgs(current)

	assertSliceContains(t, args, "--enable-rpc=true")
	assertSliceContains(t, args, "--rpc-listen-all=false")
	assertSliceContains(t, args, "--rpc-listen-port=6800")
	assertSliceContains(t, args, "--rpc-secret=secret-token")
	assertSliceContains(t, args, "--input-file=/tmp/session")
	assertSliceContains(t, args, "--save-session=/tmp/session")
	assertSliceContains(t, args, "--force-save=true")
	assertSliceContains(t, args, "--save-session-interval=60")
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

func assertSliceContains(t *testing.T, values []string, want string) {
	t.Helper()
	for _, value := range values {
		if value == want {
			return
		}
	}
	t.Fatalf("expected %v to contain %q", values, want)
}
