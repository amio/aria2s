package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validJob(root string) Job {
	return Job{ID: "0123456789abcdef", Source: "https://example.test/file", TargetDir: filepath.Join(root, "target"), TargetIdentity: ObjectIdentity{MountID: 1, ObjectID: 1}, StorageID: "fedcba9876543210", ActivityIntent: ActivityRunning, Payload: PayloadState{Location: PayloadStaging}}
}

func TestDeleteCorruptRemovesManifestDirectoryWithoutLeavingScanRow(t *testing.T) {
	repository := New(t.TempDir())
	job := validJob(t.TempDir())
	if _, err := repository.Create(job); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repository.manifestPath(job.ID), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := repository.DeleteCorrupt(job.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := repository.Scan()
	if err != nil || len(rows) != 0 {
		t.Fatalf("scan after corrupt delete = %+v err=%v", rows, err)
	}
}

func TestRepositoryCASAndCorruptScan(t *testing.T) {
	repository := New(t.TempDir())
	job := validJob(t.TempDir())
	token, err := repository.Create(job)
	if err != nil {
		t.Fatal(err)
	}
	loaded, loadedToken, err := repository.Load(job.ID)
	if err != nil || loadedToken != token {
		t.Fatalf("load: token=%v err=%v", loadedToken, err)
	}
	loaded.Execution = &ExecutionBinding{GID: "1111111111111111"}
	next, err := repository.SaveCAS(loaded, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SaveCAS(loaded, token); err == nil {
		t.Fatal("stale CAS unexpectedly succeeded")
	}
	if err := repository.DeleteCAS(job.ID, next); err != nil {
		t.Fatal(err)
	}
	rows, err := repository.Scan()
	if err != nil || len(rows) != 0 {
		t.Fatalf("scan after delete = %+v err=%v", rows, err)
	}
}

func TestRepositoryLockHonorsContext(t *testing.T) {
	repository := New(t.TempDir())
	id := "0123456789abcdef"
	unlock, err := repository.Lock(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = repository.Lock(ctx, id)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lock error = %v", err)
	}
}

func TestLoadStorageIgnoresLegacyPublicationCapability(t *testing.T) {
	root := t.TempDir()
	repository := New(root)
	id := "fedcba9876543210"
	if err := os.MkdirAll(filepath.Join(root, "storages"), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{
  "version": 1,
  "id": "fedcba9876543210",
  "mountPoint": "/mnt/storage",
  "stagingAnchor": "/mnt/storage",
  "marker": {"mountId": 1, "objectId": 2, "reliableAcrossRename": false},
  "capability": {"noReplace": false, "identityReliable": false, "directorySync": false}
}`)
	if err := os.WriteFile(filepath.Join(root, "storages", id+".json"), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	scope, err := repository.LoadStorage(id)
	if err != nil {
		t.Fatal(err)
	}
	if scope.ID != id || scope.Marker.ObjectID != 2 {
		t.Fatalf("legacy storage decoded incorrectly: %+v", scope)
	}
}

func TestJobPayloadLengthIsOptionalAndRoundTripsZero(t *testing.T) {
	repository := New(t.TempDir())
	job := validJob(t.TempDir())
	job.Payload = PayloadState{Location: PayloadPublished, Root: "empty.bin", FinalRoot: "empty.bin", Identity: ObjectIdentity{MountID: 1, ObjectID: 2}}
	length := int64(0)
	job.Payload.Length = &length
	if _, err := repository.Create(job); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := repository.Load(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Payload.Length == nil || *loaded.Payload.Length != 0 {
		t.Fatalf("payload length = %v, want known zero", loaded.Payload.Length)
	}

	legacy := validJob(t.TempDir())
	legacy.ID = "1111111111111111"
	if _, err := repository.Create(legacy); err != nil {
		t.Fatal(err)
	}
	loaded, _, err = repository.Load(legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Payload.Length != nil {
		t.Fatalf("legacy payload length = %v, want unknown", loaded.Payload.Length)
	}
}

func TestRenamedPublishedRootRoundTripsAndRequiresStoppedIntent(t *testing.T) {
	repository := New(t.TempDir())
	job := validJob(t.TempDir())
	job.ActivityIntent = ActivityStopped
	job.Payload = PayloadState{Location: PayloadPublished, Root: "Comics", FinalRoot: "Comics (1)", Identity: ObjectIdentity{MountID: 1, ObjectID: 2}}
	if _, err := repository.Create(job); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := repository.Load(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.FinalRoot() != job.Payload.FinalRoot || !loaded.PublicationRenamed() {
		t.Fatalf("renamed publication facts=%+v", loaded)
	}

	job.ID = "1111111111111111"
	job.ActivityIntent = ActivityRunning
	if _, err := repository.Create(job); err == nil {
		t.Fatal("running renamed publication was accepted")
	}
}

func TestMixedManifestVersionsMigrateLazilyWithoutChangingStorageSchema(t *testing.T) {
	root := t.TempDir()
	repository := New(root)
	legacyID := "1111111111111111"
	legacyDir := filepath.Join(root, "jobs", legacyID)
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"version":1,"id":"1111111111111111","source":"https://example.test/x","targetDir":"/tmp/target","targetIdentity":{"mountId":1,"objectId":2,"reliableAcrossRename":true},"storageId":"2222222222222222","phase":"staged","activityIntent":"running","problemCode":"RestartStateMissing","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}`
	manifest := filepath.Join(legacyDir, "manifest.json")
	if err := os.WriteFile(manifest, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	job, token, err := repository.Load(legacyID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Version != CurrentManifestVersion || job.Execution == nil || job.Execution.GID != legacyID || job.Issue == nil {
		t.Fatalf("v1 conversion = %+v", job)
	}
	if _, err := repository.SaveCAS(job, token); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(data)
	if !strings.Contains(encoded, `"version": 2`) || strings.Contains(encoded, `"phase"`) || strings.Contains(encoded, `"problemCode"`) {
		t.Fatalf("lazy v2 write retained legacy workflow fields: %s", encoded)
	}
	scope := StorageScope{ID: "3333333333333333", MountPoint: "/tmp", StagingAnchor: "/tmp", Marker: ObjectIdentity{MountID: 1, ObjectID: 3}}
	if err := repository.SaveStorage(scope); err != nil {
		t.Fatal(err)
	}
	storage, err := os.ReadFile(filepath.Join(root, "storages", scope.ID+".json"))
	if err != nil || !strings.Contains(string(storage), `"version": 1`) {
		t.Fatalf("storage schema changed: %s err=%v", storage, err)
	}
}

func TestRepositoryRejectsInvalidLegacyPhaseAndStorageVersion(t *testing.T) {
	root := t.TempDir()
	repository := New(root)
	id := "4444444444444444"
	directory := filepath.Join(root, "jobs", id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"version":1,"id":"4444444444444444","source":"https://example.test/x","targetDir":"/tmp/target","targetIdentity":{"mountId":1,"objectId":2},"storageId":"5555555555555555","phase":"unknown","activityIntent":"running"}`
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.Load(id); err == nil {
		t.Fatal("invalid legacy phase was accepted")
	}
	scope := StorageScope{Version: 2, ID: "6666666666666666", MountPoint: "/tmp", StagingAnchor: "/tmp"}
	if err := repository.SaveStorage(scope); err == nil {
		t.Fatal("unsupported storage version was written")
	}
}
