package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/amio/aria2s/internal/jobs"
	"github.com/amio/aria2s/internal/publication"
	"golang.org/x/sys/unix"
)

func TestEnsureStorageScopeCreatesAndReusesWritableMountRootScope(t *testing.T) {
	mountPoint := t.TempDir()
	targetPath := filepath.Join(mountPoint, "Books", "Comics")
	if err := os.MkdirAll(targetPath, 0o700); err != nil {
		t.Fatal(err)
	}
	repository := jobs.New(t.TempDir())
	legacy := saveTestStorageScope(t, repository, "1111111111111111", mountPoint, filepath.Join(mountPoint, "Books"))

	target := publication.Target{Path: targetPath, MountPoint: mountPoint}
	scope, err := ensureStorageScope(repository, target)
	if err != nil {
		t.Fatal(err)
	}
	if scope.ID == legacy.ID {
		t.Fatal("new job reused a noncanonical scope on a writable mount root")
	}
	if scope.StagingAnchor != mountPoint {
		t.Fatalf("staging anchor = %q, want %q", scope.StagingAnchor, mountPoint)
	}
	if _, err := os.Stat(filepath.Join(mountPoint, ".aria2s_staging", scope.ID)); err != nil {
		t.Fatalf("canonical staging root: %v", err)
	}

	reused, err := ensureStorageScope(repository, target)
	if err != nil {
		t.Fatal(err)
	}
	if reused.ID != scope.ID {
		t.Fatalf("reused scope = %q, want %q", reused.ID, scope.ID)
	}
	scopes, err := repository.ScanStorages()
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 2 {
		t.Fatalf("storage scopes = %d, want legacy plus canonical", len(scopes))
	}
}

func TestEnsureStorageScopeReusesExistingScopeWhenMountRootIsNotWritable(t *testing.T) {
	mountPoint := t.TempDir()
	targetPath := filepath.Join(mountPoint, "User", "Downloads")
	if err := os.MkdirAll(targetPath, 0o700); err != nil {
		t.Fatal(err)
	}
	repository := jobs.New(t.TempDir())
	existing := saveTestStorageScope(t, repository, "1111111111111111", mountPoint, filepath.Dir(targetPath))
	if err := os.Chmod(mountPoint, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(mountPoint, 0o700) })
	if unix.Access(mountPoint, unix.W_OK) == nil {
		t.Skip("test process can write through restrictive directory permissions")
	}

	scope, err := ensureStorageScope(repository, publication.Target{Path: targetPath, MountPoint: mountPoint})
	if err != nil {
		t.Fatal(err)
	}
	if scope.ID != existing.ID {
		t.Fatalf("scope = %q, want existing %q", scope.ID, existing.ID)
	}
}

func TestEnsureStorageScopeCreatesBesideTargetWhenMountRootIsNotWritable(t *testing.T) {
	mountPoint := t.TempDir()
	targetPath := filepath.Join(mountPoint, "User", "Downloads")
	if err := os.MkdirAll(targetPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(mountPoint, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(mountPoint, 0o700) })
	if unix.Access(mountPoint, unix.W_OK) == nil {
		t.Skip("test process can write through restrictive directory permissions")
	}

	scope, err := ensureStorageScope(jobs.New(t.TempDir()), publication.Target{Path: targetPath, MountPoint: mountPoint})
	if err != nil {
		t.Fatal(err)
	}
	wantAnchor := filepath.Dir(targetPath)
	if scope.StagingAnchor != wantAnchor {
		t.Fatalf("staging anchor = %q, want %q", scope.StagingAnchor, wantAnchor)
	}
	if _, err := os.Stat(filepath.Join(wantAnchor, ".aria2s_staging", scope.ID)); err != nil {
		t.Fatalf("fallback staging root: %v", err)
	}
}

func saveTestStorageScope(t *testing.T, repository *jobs.Repository, id, mountPoint, anchor string) jobs.StorageScope {
	t.Helper()
	root := filepath.Join(anchor, ".aria2s_staging", id)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	marker, err := publication.Identify(root)
	if err != nil {
		t.Fatal(err)
	}
	scope := jobs.StorageScope{ID: id, MountPoint: mountPoint, StagingAnchor: anchor, Marker: jobIdentity(marker)}
	if err := repository.SaveStorage(scope); err != nil {
		t.Fatal(err)
	}
	return scope
}
