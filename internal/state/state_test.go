package state_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/amio/aria2s/internal/paths"
	"github.com/amio/aria2s/internal/state"
)

func TestSaveStateWrites0600AndRoundTrips(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	current := state.State{
		Aria2cPath:   "/opt/homebrew/bin/aria2c",
		RPCPort:      6800,
		RPCSecret:    "secret-token",
		SessionPath:  servicePaths.SessionFile,
		LogPath:      servicePaths.LogFile,
		ErrorLogPath: servicePaths.ErrorLogFile,
		ServiceName:  "io.github.amio.aria2s",
	}

	if err := state.Save(servicePaths.StateFile, current); err != nil {
		t.Fatalf("save state: %v", err)
	}

	info, err := os.Stat(servicePaths.StateFile)
	if err != nil {
		t.Fatalf("stat state: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("expected 0600, got %o", got)
	}

	reloaded, err := state.Load(servicePaths.StateFile)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if !reflect.DeepEqual(reloaded, current) {
		t.Fatalf("round trip mismatch:\nwant %#v\n got %#v", current, reloaded)
	}
}
