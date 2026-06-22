package paths_test

import (
	"path/filepath"
	"testing"

	"github.com/amio/aria2s/internal/paths"
)

func TestNewDarwinBuildsStage1PathsUnderHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")

	got := paths.NewDarwin(home)

	assertEqual(t, got.ServiceName, "io.github.amio.aria2s")
	assertEqual(t, got.ServiceFile, filepath.Join(home, "Library", "LaunchAgents", "io.github.amio.aria2s.plist"))
	assertEqual(t, got.ConfigFile, filepath.Join(home, "Library", "Application Support", "aria2s", "aria2.conf"))
	assertEqual(t, got.StateFile, filepath.Join(home, "Library", "Application Support", "aria2s", "state.json"))
	assertEqual(t, got.SessionFile, filepath.Join(home, "Library", "Application Support", "aria2s", "session"))
	assertEqual(t, got.LogFile, filepath.Join(home, "Library", "Logs", "aria2s", "aria2.log"))
	assertEqual(t, got.ErrorLogFile, filepath.Join(home, "Library", "Logs", "aria2s", "aria2.err.log"))
}

func assertEqual(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
