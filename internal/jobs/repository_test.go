package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func validJob(root string) Job {
	return Job{ID: "0123456789abcdef", Source: "https://example.test/file", TargetDir: filepath.Join(root, "target"), TargetIdentity: ObjectIdentity{MountID: 1, ObjectID: 1}, StorageID: "fedcba9876543210", Phase: PhasePending, ActivityIntent: ActivityRunning}
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
	loaded.Phase = PhaseStaged
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
