package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/amio/aria2s/internal/jobs"
)

type fakeStorageReconnecter struct {
	reconnectURL string
	mounted      bool
	observeErr   error
	requestErr   error
	requests     []string
	onRequest    func(context.Context)
}

func (connector *fakeStorageReconnecter) Observe(string) (string, bool, error) {
	return connector.reconnectURL, connector.mounted, connector.observeErr
}

func (connector *fakeStorageReconnecter) Request(ctx context.Context, reconnectURL string) error {
	connector.requests = append(connector.requests, reconnectURL)
	if connector.onRequest != nil {
		connector.onRequest(ctx)
	}
	if connector.requestErr == nil && connector.onRequest == nil {
		connector.mounted = true
	}
	return connector.requestErr
}

func TestNormalizeSMBReconnectURLStripsPassword(t *testing.T) {
	tests := map[string]string{
		"//nas.local/Public":                     "smb://nas.local/Public",
		"//user@nas.local/Public":                "smb://user@nas.local/Public",
		"smb://user:secret@nas.local/Public":     "smb://user@nas.local/Public",
		"//user:secret@nas.local/My Share":       "smb://user@nas.local/My%20Share",
		"SMB://user:secret@nas.local/My%20Share": "smb://user@nas.local/My%20Share",
	}
	for source, want := range tests {
		got, err := normalizeSMBReconnectURL(source)
		if err != nil || got != want {
			t.Errorf("normalize %q = %q, %v; want %q", source, got, err, want)
		}
	}
	for _, source := range []string{"", "https://nas.local/Public", "smb://nas.local", "smb:///Public", "smb://nas.local/Public?", "smb://nas.local/Public?x=1"} {
		if got, err := normalizeSMBReconnectURL(source); err == nil {
			t.Errorf("invalid source %q normalized to %q", source, got)
		}
	}
}

func TestAddPersistsObservedStorageReconnectURL(t *testing.T) {
	application, repository, _, target := newReconcilerTestApp(t)
	connector := &fakeStorageReconnecter{mounted: true, reconnectURL: "smb://user@nas.local/Public"}
	application.options.StorageReconnecter = connector

	result, err := application.AddManaged(context.Background(), AddRequest{Source: "https://example.test/payload.bin", TargetDir: target})
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := repository.Load(result.Task.JobID)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := repository.LoadStorage(job.StorageID)
	if err != nil {
		t.Fatal(err)
	}
	if scope.ReconnectURL != connector.reconnectURL {
		t.Fatalf("reconnect URL = %q, want %q", scope.ReconnectURL, connector.reconnectURL)
	}
}

func TestRetryReconnectsBeforeLockedReconciliation(t *testing.T) {
	application, repository, rpc, target := newReconcilerTestApp(t)
	result, err := application.AddManaged(context.Background(), AddRequest{Source: "https://example.test/payload.bin", TargetDir: target})
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := repository.Load(result.Task.JobID)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := repository.LoadStorage(job.StorageID)
	if err != nil {
		t.Fatal(err)
	}
	scope.ReconnectURL = "smb://nas.local/Public"
	if err := repository.SaveStorage(scope); err != nil {
		t.Fatal(err)
	}
	status := rpc.statuses[job.Execution.GID]
	status.Status = "error"
	rpc.statuses[job.Execution.GID] = status

	connector := &fakeStorageReconnecter{reconnectURL: scope.ReconnectURL}
	connector.onRequest = func(ctx context.Context) {
		unlock, lockErr := repository.Lock(ctx, job.ID)
		if lockErr != nil {
			t.Errorf("Retry held the job lock while requesting mount: %v", lockErr)
			return
		}
		if unlockErr := unlock(); unlockErr != nil {
			t.Errorf("unlock probe: %v", unlockErr)
		}
		connector.mounted = true
	}
	application.options.StorageReconnecter = connector

	if err := application.RetryManaged(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if len(connector.requests) != 1 || connector.requests[0] != scope.ReconnectURL {
		t.Fatalf("mount requests = %v", connector.requests)
	}
}

func TestRetryDoesNotReconnectOverMountedIdentityMismatch(t *testing.T) {
	application, repository, _, target := newReconcilerTestApp(t)
	result, err := application.AddManaged(context.Background(), AddRequest{Source: "https://example.test/payload.bin", TargetDir: target})
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := repository.Load(result.Task.JobID)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := repository.LoadStorage(job.StorageID)
	if err != nil {
		t.Fatal(err)
	}
	scope.ReconnectURL = "smb://nas.local/Public"
	if err := repository.SaveStorage(scope); err != nil {
		t.Fatal(err)
	}
	storageRoot := filepath.Join(scope.StagingAnchor, ".aria2s_staging", scope.ID)
	if err := os.Rename(storageRoot, storageRoot+".replaced"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(storageRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	connector := &fakeStorageReconnecter{mounted: true, reconnectURL: scope.ReconnectURL}
	application.options.StorageReconnecter = connector

	if err := application.RetryManaged(context.Background(), job.ID); err == nil {
		t.Fatal("mounted storage identity mismatch was accepted")
	}
	if len(connector.requests) != 0 {
		t.Fatalf("identity mismatch triggered mount requests: %v", connector.requests)
	}
}

func TestRetryReconnectWaitHonorsCancellation(t *testing.T) {
	application, repository, _, target := newReconcilerTestApp(t)
	result, err := application.AddManaged(context.Background(), AddRequest{Source: "https://example.test/payload.bin", TargetDir: target})
	if err != nil {
		t.Fatal(err)
	}
	job, _, _ := repository.Load(result.Task.JobID)
	scope, _ := repository.LoadStorage(job.StorageID)
	scope.ReconnectURL = "smb://nas.local/Public"
	if err := repository.SaveStorage(scope); err != nil {
		t.Fatal(err)
	}
	connector := &fakeStorageReconnecter{reconnectURL: scope.ReconnectURL, onRequest: func(context.Context) {}}
	application.options.StorageReconnecter = connector
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := application.RetryManaged(ctx, job.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("Retry cancellation = %v", err)
	}
	if len(connector.requests) != 1 {
		t.Fatalf("mount requests = %v", connector.requests)
	}
}

func TestRetryWithoutReconnectURLKeepsExistingBehavior(t *testing.T) {
	application, _, _, target := newReconcilerTestApp(t)
	result, err := application.AddManaged(context.Background(), AddRequest{Source: "https://example.test/payload.bin", TargetDir: target})
	if err != nil {
		t.Fatal(err)
	}
	connector := &fakeStorageReconnecter{observeErr: errors.New("connector must not observe storage without a reconnect URL")}
	application.options.StorageReconnecter = connector
	if err := application.RetryManaged(context.Background(), result.Task.JobID); err != nil {
		t.Fatal(err)
	}
	if len(connector.requests) != 0 {
		t.Fatalf("storage without reconnect URL triggered mount: %v", connector.requests)
	}
}

func TestRetryRemovedJobDoesNotReconnectStorage(t *testing.T) {
	application, repository, _, target := newReconcilerTestApp(t)
	result, err := application.AddManaged(context.Background(), AddRequest{Source: "https://example.test/payload.bin", TargetDir: target})
	if err != nil {
		t.Fatal(err)
	}
	job, token, _ := repository.Load(result.Task.JobID)
	scope, _ := repository.LoadStorage(job.StorageID)
	scope.ReconnectURL = "smb://nas.local/Public"
	if err := repository.SaveStorage(scope); err != nil {
		t.Fatal(err)
	}
	job.Removed = true
	job.ActivityIntent = jobs.ActivityStopped
	if _, err := repository.SaveCAS(job, token); err != nil {
		t.Fatal(err)
	}
	connector := &fakeStorageReconnecter{reconnectURL: scope.ReconnectURL}
	application.options.StorageReconnecter = connector

	if err := application.RetryManaged(context.Background(), job.ID); err == nil {
		t.Fatal("removed job was retried")
	}
	if len(connector.requests) != 0 {
		t.Fatalf("removed job triggered mount: %v", connector.requests)
	}
}

func TestBindStorageReconnectRejectsPasswordFromProvider(t *testing.T) {
	repository := jobs.New(t.TempDir())
	scope := jobs.StorageScope{ID: "1111111111111111", MountPoint: "/Volumes/Public", StagingAnchor: "/Volumes/Public"}
	if err := repository.SaveStorage(scope); err != nil {
		t.Fatal(err)
	}
	application := New(Options{StorageReconnecter: &fakeStorageReconnecter{mounted: true, reconnectURL: "smb://user:secret@nas.local/Public"}})
	if _, err := application.bindStorageReconnect(repository, scope); err == nil {
		t.Fatal("provider password was persisted")
	}
}
