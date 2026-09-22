package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amio/aria2s/internal/app"
	"github.com/amio/aria2s/internal/paths"
	"github.com/spf13/cobra"
)

func TestLogsCommandShowsOnlyRecentOutput(t *testing.T) {
	servicePaths := paths.NewDarwin(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(servicePaths.LogFile), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(servicePaths.LogFile)
	if err != nil {
		t.Fatal(err)
	}
	tail := strings.Repeat("recent output\n", 316)
	if _, err := file.WriteAt([]byte(tail), 64*1024*1024); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(servicePaths.ErrorLogFile, []byte("last error"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := newLogsCommand(app.New(app.Options{Paths: servicePaths}))
	var output bytes.Buffer
	command.SetOut(&output)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("Logs (50 MiB startup rotation; current + .1 + .2):\n  %s\n  %s\n\nstdout:\n%s\nstderr:\nlast error\n\n", servicePaths.LogFile, servicePaths.ErrorLogFile, tail[len(tail)-4096:])
	if output.String() != want {
		t.Fatalf("unexpected logs output (%d bytes), want %d bytes", output.Len(), len(want))
	}
}

func TestPrintRecentLogEmptyAndMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aria2.log")
	var output bytes.Buffer
	command := &cobra.Command{}
	command.SetOut(&output)
	printRecentLog(command, "stdout", path)
	if got := output.String(); !strings.HasPrefix(got, "stdout:\n  unavailable: ") || !strings.Contains(got, path) || !strings.HasSuffix(got, "\n\n") {
		t.Fatalf("missing log output = %q", got)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	printRecentLog(command, "stdout", path)
	if got := output.String(); got != "stdout:\n  <empty>\n\n" {
		t.Fatalf("empty log output = %q", got)
	}
}
