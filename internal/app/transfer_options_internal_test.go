package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/jobs"
)

func TestStartupReconstructionSharesLiveTransferPolicy(t *testing.T) {
	for _, test := range []struct {
		name     string
		source   string
		metainfo bool
		control  bool
		paused   bool
	}{
		{name: "HTTP", source: "https://example.test/x"},
		{name: "magnet", source: "magnet:?xt=urn:btih:test"},
		{name: "paused magnet", source: "magnet:?xt=urn:btih:test", paused: true},
		{name: "retained torrent missing control", source: "magnet:?xt=urn:btih:test", metainfo: true, paused: true},
		{name: "retained torrent with control", source: "magnet:?xt=urn:btih:test", metainfo: true, control: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			application, repository, rpc, target := newReconcilerTestApp(t)
			added, err := application.AddManaged(context.Background(), AddRequest{Source: test.source, TargetDir: target})
			if err != nil {
				t.Fatal(err)
			}
			job, token, err := repository.Load(added.Task.JobID)
			if err != nil {
				t.Fatal(err)
			}
			scope, err := repository.LoadStorage(job.StorageID)
			if err != nil {
				t.Fatal(err)
			}
			workDir := jobs.WorkDir(scope, job.ID)
			if test.metainfo {
				if err := repository.WriteMetainfo(job.ID, []byte("d4:infod6:lengthi1e4:name1:x12:piece lengthi1e6:pieces20:01234567890123456789ee")); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(workDir, "x"), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				if test.control {
					if err := os.WriteFile(filepath.Join(workDir, "x.aria2"), []byte("control"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if test.paused {
				job.ActivityIntent = jobs.ActivityStopped
				if _, err := repository.SaveCAS(job, token); err != nil {
					t.Fatal(err)
				}
			}
			delete(rpc.statuses, job.Execution.GID)
			if _, err := application.ReconcileJob(context.Background(), job.ID, ReconcileInput{Mode: ReconcileLive}); err != nil {
				t.Fatal(err)
			}
			result, err := application.ReconcileJob(context.Background(), job.ID, ReconcileInput{Mode: ReconcileStartup})
			if err != nil || result.StartupBlock == nil {
				t.Fatalf("startup = %+v, %v", result, err)
			}
			block := *result.StartupBlock
			assertStartupMatchesAddOptions(t, block, rpc.addOptions[len(rpc.addOptions)-1])
			want := map[string]string{
				"dir": workDir, "pause": "false", "allow-overwrite": "false", "auto-file-renaming": "false",
				"follow-torrent": "false", "force-save": "true", "remove-control-file": "false",
				"bt-seed-unverified": "false", "bt-metadata-only": "false",
			}
			if test.paused {
				want["pause"] = "true"
			}
			if strings.HasPrefix(test.source, "magnet:") && !test.metainfo {
				want["bt-metadata-only"], want["bt-save-metadata"] = "true", "true"
			}
			if test.metainfo {
				if block.URI != repository.MetainfoPath(job.ID) {
					t.Fatalf("retained torrent input = %q", block.URI)
				}
				if !test.control {
					want["check-integrity"] = "true"
				}
			}
			assertSessionOptions(t, block, want)
			if _, ok := block.Option("check-integrity"); ok && (!test.metainfo || test.control) {
				t.Fatal("overrode user integrity tuning without missing control state")
			}
		})
	}
}

func TestStartupSavedOptionsPreserveTuningAndOverrideManagedPolicy(t *testing.T) {
	for _, input := range []string{"HTTP", "magnet", "retained torrent"} {
		t.Run(input, func(t *testing.T) {
			application, repository, _, target := newReconcilerTestApp(t)
			source := "magnet:?xt=urn:btih:test"
			if input == "HTTP" {
				source = "https://example.test/x"
			}
			added, err := application.AddManaged(context.Background(), AddRequest{Source: source, TargetDir: target})
			if err != nil {
				t.Fatal(err)
			}
			job, _, err := repository.Load(added.Task.JobID)
			if err != nil {
				t.Fatal(err)
			}
			scope, err := repository.LoadStorage(job.StorageID)
			if err != nil {
				t.Fatal(err)
			}
			workDir := jobs.WorkDir(scope, job.ID)
			block := aria2.SessionBlock{URI: source}
			if input != "magnet" {
				if err := os.WriteFile(filepath.Join(workDir, "x"), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if input == "retained torrent" {
				if err := repository.WriteMetainfo(job.ID, []byte("d4:infod6:lengthi1e4:name1:x12:piece lengthi1e6:pieces20:01234567890123456789ee")); err != nil {
					t.Fatal(err)
				}
				block.URI = repository.MetainfoPath(job.ID)
			}
			block.Options = []aria2.SessionOption{
				{Key: "gid", Value: job.Execution.GID}, {Key: "dir", Value: workDir},
				{Key: "pause", Value: "true"}, {Key: "bt-metadata-only", Value: "true"},
				{Key: "bt-save-metadata", Value: "false"}, {Key: "bt-seed-unverified", Value: "true"},
				{Key: "check-integrity", Value: "false"}, {Key: "max-connection-per-server", Value: "3"},
				{Key: "out", Value: "old-name"}, {Key: "future-native-option", Value: "preserved"},
			}
			if input == "magnet" {
				block.SetOption("bt-metadata-only", "false")
			}
			before := block.Clone()
			result, err := application.ReconcileJob(context.Background(), job.ID, ReconcileInput{Mode: ReconcileStartup, SavedBlock: &block})
			if err != nil || result.StartupBlock == nil {
				t.Fatalf("startup = %+v, %v", result, err)
			}
			want := map[string]string{
				"gid": job.Execution.GID, "pause": "false", "bt-metadata-only": "false",
				"bt-save-metadata": "false", "bt-seed-unverified": "false", "check-integrity": "false",
				"max-connection-per-server": "3", "future-native-option": "preserved", "out": "old-name",
			}
			switch input {
			case "HTTP":
				want["out"] = "x"
			case "magnet":
				want["bt-metadata-only"], want["bt-save-metadata"] = "true", "true"
			case "retained torrent":
				want["check-integrity"] = "true"
			}
			assertSessionOptions(t, *result.StartupBlock, want)
			if !reflect.DeepEqual(block, before) {
				t.Fatal("normalization mutated the saved input")
			}
		})
	}
}

func assertStartupMatchesAddOptions(t *testing.T, block aria2.SessionBlock, options aria2.AddOptions) {
	t.Helper()
	live := aria2.SessionBlock{}
	live.ApplyOptions(options)
	got := make(map[string]string)
	want := make(map[string]string)
	for _, option := range block.Options {
		if option.Key != "gid" {
			got[option.Key] = option.Value
		}
	}
	for _, option := range live.Options {
		if option.Key != "gid" {
			want[option.Key] = option.Value
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("startup options = %v, live options = %v", got, want)
	}
}

func assertSessionOptions(t *testing.T, block aria2.SessionBlock, want map[string]string) {
	t.Helper()
	for key, expected := range want {
		if got, ok := block.Option(key); !ok || got != expected {
			t.Errorf("session %s = %q, want %q", key, got, expected)
		}
	}
}
