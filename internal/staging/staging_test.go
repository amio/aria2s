package staging_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/staging"
)

func TestDirTargetRoundTrip(t *testing.T) {
	target := "/Volumes/SynoPub/News"
	staged := staging.Dir(target)
	if want := "/Volumes/SynoPub/.incomplete/News"; staged != want {
		t.Fatalf("Dir(%q) = %q, want %q", target, staged, want)
	}
	got, ok := staging.Target(staged)
	if !ok || got != target {
		t.Fatalf("Target(%q) = %q, %v; want %q, true", staged, got, ok, target)
	}
}

func TestTargetRejectsNonStagingPaths(t *testing.T) {
	for _, dir := range []string{
		"/Volumes/SynoPub/News",
		"/downloads/.incomplete",          // marker is the base, not a parent
		"/downloads/.incomplete/x/.trash", // marker not directly above base
		"",
	} {
		if staging.IsStaged(dir) {
			t.Fatalf("IsStaged(%q) = true, want false", dir)
		}
	}
}

type fakeRPC struct {
	detail        aria2.DownloadDetail
	detailErr     error
	locations     []aria2.TaskLocation
	pauseErr      error
	changedDir    string
	changeOptionN int
	saved         int
	paused        int
	resumed       int
}

func (fake *fakeRPC) TaskDetail(context.Context, string) (aria2.DownloadDetail, error) {
	return fake.detail, fake.detailErr
}

func (fake *fakeRPC) Pause(context.Context, string) error {
	if fake.pauseErr == nil {
		fake.paused++
	}
	return fake.pauseErr
}

func (fake *fakeRPC) Resume(context.Context, string) error {
	fake.resumed++
	return nil
}

func (fake *fakeRPC) ChangeOption(_ context.Context, _ string, options map[string]string) error {
	fake.changeOptionN++
	fake.changedDir = options["dir"]
	return nil
}

func (fake *fakeRPC) SaveSession(context.Context) error {
	fake.saved++
	return nil
}

func (fake *fakeRPC) TaskLocations(context.Context) ([]aria2.TaskLocation, error) {
	return fake.locations, nil
}

// seedTorrentDownload lays out a staged multi-file torrent on disk and returns
// the staging dir, target dir, and the matching task detail.
func seedTorrentDownload(t *testing.T) (string, string, aria2.DownloadDetail) {
	t.Helper()
	root := t.TempDir()
	target := filepath.Join(root, "News")
	staged := staging.Dir(target)

	payload := filepath.Join(staged, "Some Torrent", "episode01.mkv")
	if err := os.MkdirAll(filepath.Dir(payload), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	// bt-save-metadata stores <infohash>.torrent next to the payload; aria2
	// reports the hash uppercase while the on-disk case may differ.
	if err := os.WriteFile(filepath.Join(staged, "abcdef.torrent"), []byte("meta"), 0o644); err != nil {
		t.Fatal(err)
	}
	detail := aria2.DownloadDetail{
		GID:         "gid-1",
		Status:      "active",
		Seeder:      true,
		InfoHash:    "ABCDEF",
		DownloadDir: staged,
		Files: []aria2.DownloadFile{
			{Path: payload, Selected: true},
			{Path: filepath.Join(staged, "Some Torrent", "sample.mkv"), Selected: false},
		},
	}
	return staged, target, detail
}

func TestCompleteMovesPayloadMetadataAndRepointsDir(t *testing.T) {
	staged, target, detail := seedTorrentDownload(t)
	rpc := &fakeRPC{detail: detail}

	moved, err := (&staging.Mover{}).Complete(context.Background(), rpc, "gid-1")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !moved {
		t.Fatal("Complete returned moved=false")
	}

	assertFileContent(t, filepath.Join(target, "Some Torrent", "episode01.mkv"), "video")
	assertFileContent(t, filepath.Join(target, "abcdef.torrent"), "meta")
	if rpc.changedDir != target {
		t.Fatalf("changeOption dir = %q, want %q", rpc.changedDir, target)
	}
	if rpc.paused != 1 || rpc.resumed != 1 {
		t.Fatalf("pause/resume = %d/%d, want 1/1", rpc.paused, rpc.resumed)
	}
	if rpc.saved != 1 {
		t.Fatalf("saveSession calls = %d, want 1", rpc.saved)
	}
	// Staging shell is cleaned up once empty.
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("staging dir should be removed, stat err = %v", err)
	}
}

func TestCompleteSkipsUnfinishedAndForeignDownloads(t *testing.T) {
	_, _, detail := seedTorrentDownload(t)

	active := detail
	active.Seeder = false
	active.Status = "active"
	if moved, err := (&staging.Mover{}).Complete(context.Background(), &fakeRPC{detail: active}, "gid-1"); err != nil || moved {
		t.Fatalf("downloading task: moved=%v err=%v, want false/nil", moved, err)
	}

	foreign := detail
	foreign.Status = "complete"
	foreign.Seeder = false
	foreign.DownloadDir = "/Volumes/SynoPub/News"
	if moved, err := (&staging.Mover{}).Complete(context.Background(), &fakeRPC{detail: foreign}, "gid-1"); err != nil || moved {
		t.Fatalf("non-staged task: moved=%v err=%v, want false/nil", moved, err)
	}
}

func TestCompleteAbortsOnCollisionWithoutClobbering(t *testing.T) {
	staged, target, detail := seedTorrentDownload(t)
	existing := filepath.Join(target, "Some Torrent", "episode01.mkv")
	if err := os.MkdirAll(filepath.Dir(existing), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}

	rpc := &fakeRPC{detail: detail}
	moved, err := (&staging.Mover{}).Complete(context.Background(), rpc, "gid-1")
	if err == nil || moved {
		t.Fatalf("collision: moved=%v err=%v, want false/error", moved, err)
	}
	assertFileContent(t, existing, "keep me")
	assertFileContent(t, filepath.Join(staged, "Some Torrent", "episode01.mkv"), "video")
	if rpc.changeOptionN != 0 {
		t.Fatalf("changeOption called %d times on collision, want 0", rpc.changeOptionN)
	}
}

func TestCompleteIsIdempotentAcrossPartialMove(t *testing.T) {
	staged, target, detail := seedTorrentDownload(t)
	// Simulate a previous run that moved the payload but crashed before the
	// metadata sidecar and the changeOption call.
	movedPayload := filepath.Join(target, "Some Torrent", "episode01.mkv")
	if err := os.MkdirAll(filepath.Dir(movedPayload), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(staged, "Some Torrent", "episode01.mkv"), movedPayload); err != nil {
		t.Fatal(err)
	}

	rpc := &fakeRPC{detail: detail}
	moved, err := (&staging.Mover{}).Complete(context.Background(), rpc, "gid-1")
	if err != nil || !moved {
		t.Fatalf("retry after partial move: moved=%v err=%v, want true/nil", moved, err)
	}
	assertFileContent(t, movedPayload, "video")
	assertFileContent(t, filepath.Join(target, "abcdef.torrent"), "meta")
}

func TestCompleteResumesSeedingEvenWhenMoveFails(t *testing.T) {
	_, target, detail := seedTorrentDownload(t)
	// Make the move fail: target path exists as a file, so creating the
	// destination directory tree is impossible.
	if err := os.WriteFile(target, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	rpc := &fakeRPC{detail: detail}
	if _, err := (&staging.Mover{}).Complete(context.Background(), rpc, "gid-1"); err == nil {
		t.Fatal("expected move failure")
	}
	if rpc.paused != 1 || rpc.resumed != 1 {
		t.Fatalf("pause/resume after failure = %d/%d, want 1/1", rpc.paused, rpc.resumed)
	}
}

func TestSweepMovesOnlyFinishedStagedDownloads(t *testing.T) {
	_, _, detail := seedTorrentDownload(t)
	rpc := &fakeRPC{
		detail: detail,
		locations: []aria2.TaskLocation{
			{GID: "gid-1", Status: "complete", Dir: detail.DownloadDir},
			{GID: "gid-2", Status: "active", Dir: detail.DownloadDir}, // still downloading
			{GID: "gid-3", Status: "error", Dir: detail.DownloadDir},  // failed
			{GID: "gid-4", Status: "complete", Dir: "/data/other"},    // not staged
		},
	}
	moved, err := (&staging.Mover{}).Sweep(context.Background(), rpc)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if moved != 1 {
		t.Fatalf("Sweep moved %d downloads, want 1", moved)
	}
}

func TestSweepToleratesDetailFailures(t *testing.T) {
	rpc := &fakeRPC{
		detailErr: errors.New("rpc down"),
		locations: []aria2.TaskLocation{
			{GID: "gid-1", Status: "complete", Dir: "/data/.incomplete/x"},
		},
	}
	moved, err := (&staging.Mover{}).Sweep(context.Background(), rpc)
	if err != nil {
		t.Fatalf("Sweep should tolerate per-task failures: %v", err)
	}
	if moved != 0 {
		t.Fatalf("Sweep moved %d, want 0", moved)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if strings.TrimSpace(string(data)) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}
