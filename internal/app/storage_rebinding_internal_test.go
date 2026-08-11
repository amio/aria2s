package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/jobs"
	"github.com/amio/aria2s/internal/publication"
)

func TestEnsureStorageScopeRebindsChangedMountID(t *testing.T) {
	repository := jobs.New(t.TempDir())
	targetPath := t.TempDir()
	target, err := publication.InspectTarget(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := ensureStorageScope(repository, target)
	if err != nil {
		t.Fatal(err)
	}
	scope.StableID = ""
	scope.Marker.MountID = differentMountID(scope.Marker.MountID)
	if err := repository.SaveStorage(scope); err != nil {
		t.Fatal(err)
	}

	rebound, err := ensureStorageScope(repository, target)
	if err != nil {
		t.Fatalf("reuse after remount: %v", err)
	}
	if rebound.ID != scope.ID || rebound.StableID == "" || rebound.Marker.MountID != target.Identity.MountID {
		t.Fatalf("rebound scope = %+v, target = %+v", rebound, target.Identity)
	}
	persisted, err := repository.LoadStorage(scope.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Marker != rebound.Marker {
		t.Fatalf("persisted marker = %+v, want %+v", persisted.Marker, rebound.Marker)
	}
}

func TestLegacyStorageScopeRejectsChangedMarkerObject(t *testing.T) {
	repository := jobs.New(t.TempDir())
	target, err := publication.InspectTarget(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scope, err := ensureStorageScope(repository, target)
	if err != nil {
		t.Fatal(err)
	}
	scope.StableID = ""
	scope.Marker.ObjectID++
	if err := repository.SaveStorage(scope); err != nil {
		t.Fatal(err)
	}

	if _, err := ensureStorageScope(repository, target); err == nil {
		t.Fatal("changed staging marker was accepted as a remount")
	}
}

func TestPortableStorageMarkerRejectsChangedContents(t *testing.T) {
	anchor := t.TempDir()
	id := "4545454545454545"
	stagingRoot := filepath.Join(anchor, ".aria2s_staging", id)
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	marker, err := publication.Identify(stagingRoot)
	if err != nil {
		t.Fatal(err)
	}
	stableID := "aria2s-marker:" + id
	if err := createStorageMarker(stagingRoot, stableID); err != nil {
		t.Fatal(err)
	}
	scope := jobs.StorageScope{ID: id, MountPoint: anchor, StagingAnchor: anchor, StableID: stableID, Marker: jobIdentity(marker)}
	if _, needsBinding, err := observeStorageScope(scope); err != nil || needsBinding {
		t.Fatalf("portable marker observation = binding=%t err=%v", needsBinding, err)
	}
	if err := os.WriteFile(filepath.Join(stagingRoot, storageMarkerName), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := observeStorageScope(scope); err == nil {
		t.Fatal("changed portable marker was accepted")
	}
}

func TestStartupRebindsStagedJobMountIdentities(t *testing.T) {
	application, repository, _, targetPath := newReconcilerTestApp(t)
	result, err := application.AddManaged(context.Background(), AddRequest{Source: "https://example.test/payload.bin", TargetDir: targetPath})
	if err != nil {
		t.Fatal(err)
	}
	job, token, err := repository.Load(result.Task.JobID)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := makeStoredMountIDsStale(repository, job, token)
	if err != nil {
		t.Fatal(err)
	}
	workDir := jobs.WorkDir(scope, job.ID)
	block := aria2.SessionBlock{URI: job.Source, Options: []aria2.SessionOption{{Key: "gid", Value: job.Execution.GID}, {Key: "dir", Value: workDir}}}

	reconciled, err := application.ReconcileJob(context.Background(), job.ID, ReconcileInput{Mode: ReconcileStartup, SavedBlock: &block})
	if err != nil {
		t.Fatalf("startup after remount: %v", err)
	}
	if reconciled.StartupBlock == nil {
		t.Fatal("startup omitted the rebound staged job")
	}
	assertCurrentMountIdentities(t, repository, job.ID)
}

func TestStartupRebindsPublishedPayloadMountIdentity(t *testing.T) {
	application, repository, _, targetPath := newReconcilerTestApp(t)
	target, err := publication.InspectTarget(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := ensureStorageScope(repository, target)
	if err != nil {
		t.Fatal(err)
	}
	payloadPath := filepath.Join(targetPath, "payload.bin")
	if err := os.WriteFile(payloadPath, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	payload, err := publication.Identify(payloadPath)
	if err != nil {
		t.Fatal(err)
	}
	job := jobs.Job{
		ID:             "7878787878787878",
		Source:         "https://example.test/payload.bin",
		TargetDir:      targetPath,
		TargetIdentity: jobIdentity(target.Identity),
		StorageID:      scope.ID,
		ActivityIntent: jobs.ActivityStopped,
		Payload: jobs.PayloadState{
			Location:  jobs.PayloadPublished,
			Root:      "payload.bin",
			FinalRoot: "payload.bin",
			Identity:  jobIdentity(payload),
		},
	}
	token, err := repository.Create(job)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := makeStoredMountIDsStale(repository, job, token); err != nil {
		t.Fatal(err)
	}

	if _, err := application.ReconcileJob(context.Background(), job.ID, ReconcileInput{Mode: ReconcileStartup}); err != nil {
		t.Fatalf("published startup after remount: %v", err)
	}
	assertCurrentMountIdentities(t, repository, job.ID)
}

func makeStoredMountIDsStale(repository *jobs.Repository, job jobs.Job, token jobs.Token) (jobs.StorageScope, error) {
	scope, err := repository.LoadStorage(job.StorageID)
	if err != nil {
		return jobs.StorageScope{}, err
	}
	stale := differentMountID(scope.Marker.MountID)
	scope.Marker.MountID = stale
	if err := repository.SaveStorage(scope); err != nil {
		return jobs.StorageScope{}, err
	}
	job.TargetIdentity.MountID = stale
	if job.Payload.Identity.MountID != 0 {
		job.Payload.Identity.MountID = stale
	}
	_, err = repository.SaveCAS(job, token)
	return scope, err
}

func assertCurrentMountIdentities(t *testing.T, repository *jobs.Repository, jobID string) {
	t.Helper()
	job, _, err := repository.Load(jobID)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := repository.LoadStorage(job.StorageID)
	if err != nil {
		t.Fatal(err)
	}
	target, err := publication.InspectTarget(job.TargetDir)
	if err != nil {
		t.Fatal(err)
	}
	if scope.Marker.MountID != target.Identity.MountID || job.TargetIdentity.MountID != target.Identity.MountID {
		t.Fatalf("mount identities were not normalized: scope=%+v job=%+v current=%+v", scope.Marker, job.TargetIdentity, target.Identity)
	}
	if job.Payload.Identity.MountID != 0 && job.Payload.Identity.MountID != target.Identity.MountID {
		t.Fatalf("payload mount identity was not normalized: %+v", job.Payload.Identity)
	}
	if job.Issue != nil {
		t.Fatalf("rebound job retained issue: %+v", job.Issue)
	}
}

func differentMountID(current uint64) uint64 {
	if current == ^uint64(0) {
		return current - 1
	}
	return current + 1
}
