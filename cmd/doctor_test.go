package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amio/aria2s/internal/app"
	"github.com/amio/aria2s/internal/paths"
)

func TestDoctorJSONFailureIsOneDocumentAndReadOnly(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "missing-home"))
	application := app.New(app.Options{Paths: servicePaths})
	for _, args := range [][]string{{"doctor", "--json"}, {"doctor", "--json", "--repair"}} {
		var stdout, stderr bytes.Buffer
		command := NewRoot(application)
		command.SetOut(&stdout)
		command.SetErr(&stderr)
		command.SetArgs(args)
		if err := command.Execute(); !IsReportedFailure(err) {
			t.Fatalf("want reported failure, got %v", err)
		}
		var report struct {
			Healthy, Complete bool
			Checks            []any
		}
		if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
			t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout.String())
		}
		if report.Healthy || report.Complete || len(report.Checks) == 0 {
			t.Fatalf("invalid failure report: %+v", report)
		}
		if strings.Contains(stdout.String(), "\x1b") {
			t.Fatal("ANSI in redirected output")
		}
		if _, err := os.Stat(servicePaths.StateDir); !os.IsNotExist(err) {
			t.Fatalf("diagnosis created state directory: %v", err)
		}
	}
}

func TestDoctorRejectsInvalidFlagsBeforeInspecting(t *testing.T) {
	application := app.New(app.Options{Paths: paths.NewDarwin(filepath.Join(t.TempDir(), "home"))})
	for _, args := range [][]string{
		{"doctor", "--discard-unmanaged-tasks"}, {"doctor", "--json", "--summary"},
		{"doctor", "--summary", "--all"}, {"doctor", "unexpected"},
	} {
		var output bytes.Buffer
		command := NewRoot(application)
		command.SetOut(&output)
		command.SetErr(&output)
		command.SetArgs(args)
		if err := command.Execute(); err == nil {
			t.Fatalf("accepted %v", args)
		}
		if output.Len() != 0 {
			t.Fatalf("inspection ran for invalid flags: %s", output.String())
		}
	}
}
