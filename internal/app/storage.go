package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/amio/aria2s/internal/jobs"
	"github.com/amio/aria2s/internal/publication"
)

const storageMarkerName = ".aria2s-storage"

// observeStorageScope treats MountID as a mount-session fact. A native volume
// UUID or portable marker is the durable storage identity; the legacy marker
// object is accepted only once to bootstrap that stable identity.
func observeStorageScope(scope jobs.StorageScope) (jobs.StorageScope, bool, error) {
	stagingRoot := filepath.Join(scope.StagingAnchor, ".aria2s_staging", scope.ID)
	observed, err := publication.InspectTarget(stagingRoot)
	if err != nil {
		return jobs.StorageScope{}, false, err
	}
	needsStableBinding := scope.StableID == ""
	if needsStableBinding {
		if observed.Identity.ObjectID != scope.Marker.ObjectID {
			return jobs.StorageScope{}, false, errors.New("legacy staging marker object changed")
		}
	} else if strings.HasPrefix(scope.StableID, "aria2s-marker:") {
		if err := validateStorageMarker(stagingRoot, scope.StableID); err != nil {
			return jobs.StorageScope{}, false, err
		}
	} else {
		stableID, supported, err := publication.StableStorageID(stagingRoot)
		if err != nil {
			return jobs.StorageScope{}, false, err
		}
		if !supported || stableID != scope.StableID {
			return jobs.StorageScope{}, false, errors.New("registered volume identity changed")
		}
		if observed.Identity.ObjectID != scope.Marker.ObjectID {
			return jobs.StorageScope{}, false, errors.New("registered staging marker object changed")
		}
	}
	scope.Marker = jobIdentity(observed.Identity)
	return scope, needsStableBinding, nil
}

func bindStableStorageIdentity(scope jobs.StorageScope) (jobs.StorageScope, error) {
	stagingRoot := filepath.Join(scope.StagingAnchor, ".aria2s_staging", scope.ID)
	stableID, supported, err := publication.StableStorageID(stagingRoot)
	if err != nil {
		return jobs.StorageScope{}, err
	}
	if supported {
		scope.StableID = stableID
		return scope, nil
	}
	scope.StableID = "aria2s-marker:" + scope.ID
	if err := createStorageMarker(stagingRoot, scope.StableID); err != nil {
		return jobs.StorageScope{}, err
	}
	return scope, nil
}

func commitStorageObservation(repository *jobs.Repository, stored, observed jobs.StorageScope, needsStableBinding bool) (jobs.StorageScope, error) {
	var err error
	if needsStableBinding {
		observed, err = bindStableStorageIdentity(observed)
		if err != nil {
			return jobs.StorageScope{}, err
		}
	}
	if observed != stored {
		if err := repository.SaveStorage(observed); err != nil {
			return jobs.StorageScope{}, err
		}
	}
	return observed, nil
}

func loadObservedStorageScope(repository *jobs.Repository, id string) (jobs.StorageScope, error) {
	stored, err := repository.LoadStorage(id)
	if err != nil {
		return jobs.StorageScope{}, err
	}
	observed, needsStableBinding, err := observeStorageScope(stored)
	if err != nil {
		return jobs.StorageScope{}, err
	}
	return commitStorageObservation(repository, stored, observed, needsStableBinding)
}

// rebindJobStorage normalizes persisted mount-session identities only after
// both the app-owned staging marker and the registered target independently
// prove that the original storage scope is mounted at its original path.
func rebindJobStorage(repository *jobs.Repository, job jobs.Job, token jobs.Token) (jobs.StorageScope, jobs.Job, jobs.Token, error) {
	storedScope, err := repository.LoadStorage(job.StorageID)
	if err != nil {
		return jobs.StorageScope{}, job, token, err
	}
	observedScope, needsStableBinding, err := observeStorageScope(storedScope)
	if err != nil {
		return jobs.StorageScope{}, job, token, err
	}

	target, err := publication.InspectTarget(job.TargetDir)
	if err != nil {
		return jobs.StorageScope{}, job, token, err
	}
	if filepath.Clean(target.MountPoint) != filepath.Clean(observedScope.MountPoint) {
		return jobs.StorageScope{}, job, token, errors.New("registered target resolves to a different mount point")
	}
	if target.Identity.MountID != observedScope.Marker.MountID {
		return jobs.StorageScope{}, job, token, errors.New("registered target and staging marker are on different mounts")
	}
	if target.Identity.ObjectID != job.TargetIdentity.ObjectID {
		return jobs.StorageScope{}, job, token, errors.New("registered target object changed")
	}

	normalized := job
	normalized.TargetIdentity = jobIdentity(target.Identity)
	if normalized.Payload.Identity.MountID != 0 {
		registeredPayloadMount := normalized.Payload.Identity.MountID
		if registeredPayloadMount != job.TargetIdentity.MountID &&
			registeredPayloadMount != storedScope.Marker.MountID &&
			registeredPayloadMount != target.Identity.MountID {
			return jobs.StorageScope{}, job, token, errors.New("registered payload belongs to a different mount")
		}
		normalized.Payload.Identity.MountID = target.Identity.MountID
	}

	// Compute every external fact before writing a stable binding or normalizing
	// either atomic control file. A later job can finish a partial normalization.
	observedScope, err = commitStorageObservation(repository, storedScope, observedScope, needsStableBinding)
	if err != nil {
		return jobs.StorageScope{}, job, token, err
	}
	if normalized.TargetIdentity != job.TargetIdentity || normalized.Payload.Identity != job.Payload.Identity {
		next, err := repository.SaveCAS(normalized, token)
		if err != nil {
			return jobs.StorageScope{}, job, token, err
		}
		token = next
	}
	return observedScope, normalized, token, nil
}

func createStorageMarker(stagingRoot, stableID string) error {
	path := filepath.Join(stagingRoot, storageMarkerName)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return validateStorageMarker(stagingRoot, stableID)
	}
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = os.Remove(path)
		}
	}()
	if _, err := fmt.Fprintln(file, stableID); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	committed = true
	return nil
}

func validateStorageMarker(stagingRoot, stableID string) error {
	path := filepath.Join(stagingRoot, storageMarkerName)
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 256 {
		return errors.New("registered storage marker is not a regular bounded file")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return errors.New("registered storage marker changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, 257))
	if err != nil {
		return err
	}
	if string(data) != stableID+"\n" {
		return errors.New("registered storage marker changed")
	}
	return nil
}

func cleanupNewStorageRoot(root string) error {
	markerErr := os.Remove(filepath.Join(root, storageMarkerName))
	if errors.Is(markerErr, os.ErrNotExist) {
		markerErr = nil
	}
	return errors.Join(markerErr, os.Remove(root))
}
