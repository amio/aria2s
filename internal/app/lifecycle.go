package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/atomicfile"
	"github.com/amio/aria2s/internal/jobs"
	"github.com/amio/aria2s/internal/publication"
	managedruntime "github.com/amio/aria2s/internal/runtime"
	"github.com/amio/aria2s/internal/state"
	"golang.org/x/sys/unix"
)

type managedRPC interface {
	AddURI(context.Context, state.State, string, aria2.AddOptions) (string, error)
	AddTorrent(context.Context, state.State, []byte, aria2.AddOptions) (string, error)
	LifecycleStatus(context.Context, state.State, string) (aria2.LifecycleStatus, error)
	Pause(context.Context, state.State, string) error
	Resume(context.Context, state.State, string) error
	ForceRemove(context.Context, state.State, string) error
	RemoveDownloadResult(context.Context, state.State, string) error
	SaveSession(context.Context, state.State) error
}

func (app *App) SetActivity(ctx context.Context, gid string, running bool) error {
	repository := jobs.New(app.options.Paths.StateDir)
	unlock, err := repository.Lock(ctx, gid)
	if err != nil {
		return err
	}
	defer unlock()
	job, token, err := repository.Load(gid)
	if err != nil {
		return err
	}
	if job.Phase == jobs.PhaseRemoved || job.Phase == jobs.PhasePublishing {
		return errors.New("activity cannot change in the current publication phase")
	}
	intent := jobs.ActivityStopped
	if running {
		intent = jobs.ActivityRunning
	}
	job.ActivityIntent = intent
	token, err = repository.SaveCAS(job, token)
	if err != nil {
		return err
	}
	current, err := state.Load(app.options.Paths.StateFile)
	if err != nil {
		return err
	}
	rpc, ok := app.options.RPC.(managedRPC)
	if !ok {
		return errors.New("configured RPC does not support managed activity")
	}
	if job.Phase == jobs.PhaseStaged {
		if running {
			err = rpc.Resume(ctx, current, gid)
		} else {
			err = rpc.Pause(ctx, current, gid)
		}
		if err != nil && !aria2.IsNotFound(err) {
			return err
		}
		if checkpointErr := rpc.SaveSession(ctx, current); checkpointErr != nil {
			return fmt.Errorf("RestartCheckpointFailed: %w", checkpointErr)
		}
		return nil
	}
	if job.Phase == jobs.PhasePublished {
		if running {
			return app.startFinalSeedWithoutLock(ctx, repository, job)
		}
		if removeErr := rpc.ForceRemove(ctx, current, gid); removeErr != nil && !aria2.IsNotFound(removeErr) {
			return removeErr
		}
		if clearErr := rpc.RemoveDownloadResult(ctx, current, gid); clearErr != nil && !aria2.IsNotFound(clearErr) {
			return clearErr
		}
	}
	return nil
}

func (app *App) RemoveManaged(ctx context.Context, gid string) error {
	repository := jobs.New(app.options.Paths.StateDir)
	unlock, err := repository.Lock(ctx, gid)
	if err != nil {
		return err
	}
	defer unlock()
	job, token, err := repository.Load(gid)
	if err != nil {
		return err
	}
	if job.Phase == jobs.PhasePublishing {
		return errors.New("uncertain publication must reconcile before removal")
	}
	previous := job.Phase
	job.Phase = jobs.PhaseRemoved
	job.ActivityIntent = jobs.ActivityStopped
	token, err = repository.SaveCAS(job, token)
	if err != nil {
		return err
	}
	current, err := state.Load(app.options.Paths.StateFile)
	if err != nil {
		return err
	}
	rpc, ok := app.options.RPC.(managedRPC)
	if !ok {
		return errors.New("configured RPC does not support managed removal")
	}
	if err := detachManagedNative(ctx, rpc, current, gid); err != nil {
		return err
	}
	if previous == jobs.PhasePending || previous == jobs.PhaseStaged {
		scope, loadErr := repository.LoadStorage(job.StorageID)
		if loadErr != nil {
			return loadErr
		}
		if !storageMatches(scope, job) {
			return persistJobProblem(repository, job, token, "CleanupFailed", errors.New("StorageMismatch: refusing staging cleanup on changed storage"))
		}
		if removeErr := removeWorkDir(jobs.WorkDir(scope, gid)); removeErr != nil {
			return persistJobProblem(repository, job, token, "CleanupFailed", removeErr)
		}
	}
	return nil
}

func (app *App) ClearManaged(ctx context.Context, gid string) error {
	repository := jobs.New(app.options.Paths.StateDir)
	unlock, err := repository.Lock(ctx, gid)
	if err != nil {
		return err
	}
	defer unlock()
	job, token, err := repository.Load(gid)
	if err != nil {
		if !repository.Exists(gid) {
			return err
		}
		current, stateErr := state.Load(app.options.Paths.StateFile)
		if stateErr != nil {
			return stateErr
		}
		rpc, ok := app.options.RPC.(managedRPC)
		if !ok {
			return errors.New("cannot prove native absence for corrupt managed metadata")
		}
		if native, statusErr := rpc.LifecycleStatus(ctx, current, gid); statusErr == nil && native.GID != "" {
			return errors.New("corrupt managed task is still present in aria2")
		} else if statusErr != nil && !aria2.IsNotFound(statusErr) {
			return statusErr
		}
		return repository.DeleteCorrupt(gid)
	}
	current, err := state.Load(app.options.Paths.StateFile)
	if err != nil {
		return err
	}
	rpc, rpcOK := app.options.RPC.(managedRPC)
	if rpcOK {
		if native, statusErr := rpc.LifecycleStatus(ctx, current, gid); statusErr == nil && native.GID != "" {
			return errors.New("managed task is still present in aria2")
		} else if statusErr != nil && !aria2.IsNotFound(statusErr) {
			return statusErr
		}
	}
	if job.Phase == jobs.PhasePublishing {
		if !rpcOK {
			return errors.New("cannot prove native absence for publication recovery")
		}
		return errors.New("publication must reconcile with Retry before Clear")
	}
	if job.ProblemCode == "CleanupFailed" {
		return errors.New("managed cleanup must succeed before Clear")
	}
	if job.Phase == jobs.PhaseRemoved && job.ProblemCode != "" {
		return errors.New("removed task cleanup must succeed before Clear")
	}
	if job.Phase == jobs.PhaseRemoved {
		scope, loadErr := repository.LoadStorage(job.StorageID)
		if loadErr != nil {
			return loadErr
		}
		if _, workErr := os.Lstat(jobs.WorkDir(scope, gid)); workErr == nil {
			return errors.New("removed staging artifacts must be cleaned with Retry before Clear")
		} else if !errors.Is(workErr, os.ErrNotExist) {
			return workErr
		}
	}
	return repository.DeleteCAS(gid, token)
}

func (app *App) RetryManaged(ctx context.Context, gid string) error {
	repository := jobs.New(app.options.Paths.StateDir)
	unlock, err := repository.Lock(ctx, gid)
	if err != nil {
		return err
	}
	defer unlock()
	job, token, err := repository.Load(gid)
	if err != nil {
		return err
	}
	scope, err := repository.LoadStorage(job.StorageID)
	if err != nil {
		return err
	}
	if !storageIdentityMatches(scope, job) {
		return errors.New("StorageOffline: registered storage is unavailable or mismatched")
	}
	if job.Phase == jobs.PhaseRemoved {
		current, loadErr := state.Load(app.options.Paths.StateFile)
		if loadErr != nil {
			return loadErr
		}
		rpc, ok := app.options.RPC.(managedRPC)
		if !ok {
			return errors.New("configured RPC does not support removed-task Retry")
		}
		if err := detachManagedNative(ctx, rpc, current, gid); err != nil {
			return err
		}
		workDir := jobs.WorkDir(scope, gid)
		var cleanupErr error
		if job.PayloadIdentity.ObjectID == 0 {
			cleanupErr = removeWorkDir(workDir)
		} else {
			cleanupErr = cleanupPublishedWorkDir(workDir, job.PayloadRoot)
		}
		if cleanupErr != nil {
			return persistJobProblem(repository, job, token, "CleanupFailed", cleanupErr)
		}
		job.ProblemCode = ""
		_, err = repository.SaveCAS(job, token)
		return err
	}
	if job.Phase == jobs.PhaseStaged && job.ProblemCode != "" {
		return app.retryStagedWithoutLock(ctx, repository, job, token, scope)
	}
	if job.Phase == jobs.PhasePublished && job.ProblemCode != "" && job.ProblemCode != "PowerLossDurabilityUnavailable" {
		if job.ActivityIntent == jobs.ActivityRunning {
			current, loadErr := state.Load(app.options.Paths.StateFile)
			if loadErr != nil {
				return loadErr
			}
			rpc, ok := app.options.RPC.(managedRPC)
			if !ok {
				return errors.New("configured RPC does not support final-seed Retry")
			}
			native, statusErr := rpc.LifecycleStatus(ctx, current, gid)
			destination := filepath.Join(job.TargetDir, job.PayloadRoot)
			switch {
			case statusErr == nil:
				if native.GID != gid || filepath.Clean(native.Dir) != filepath.Clean(job.TargetDir) || !publishedFilesMatch(native.Files, destination) {
					return errors.New("ManagedIdentityConflict: existing final seed does not match the published payload")
				}
			case aria2.IsNotFound(statusErr):
				if err := app.setActivityWithoutLock(ctx, repository, job, true); err != nil {
					return err
				}
			default:
				return statusErr
			}
		}
		job, token, err = repository.Load(gid)
		if err != nil {
			return err
		}
		if cleanupErr := cleanupPublishedWorkDir(jobs.WorkDir(scope, gid), job.PayloadRoot); cleanupErr != nil {
			return persistJobProblem(repository, job, token, "CleanupFailed", cleanupErr)
		}
		job.ProblemCode = ""
		_, err = repository.SaveCAS(job, token)
		return err
	}
	if job.Phase == jobs.PhasePublishing {
		current, loadErr := state.Load(app.options.Paths.StateFile)
		if loadErr != nil {
			return loadErr
		}
		rpc, ok := app.options.RPC.(managedRPC)
		if !ok {
			return errors.New("cannot prove native absence before publication Retry")
		}
		if native, statusErr := rpc.LifecycleStatus(ctx, current, gid); statusErr == nil && native.GID != "" {
			if err := detachManagedNative(ctx, rpc, current, gid); err != nil {
				return err
			}
		} else if statusErr != nil && !aria2.IsNotFound(statusErr) {
			return statusErr
		}
		job, token, err = reconcilePublishing(repository, job, token, scope)
		if err != nil {
			return err
		}
		if job.Phase != jobs.PhasePublished {
			return errors.New(job.ProblemCode + ": publication remains unresolved")
		}
		if job.ActivityIntent == jobs.ActivityRunning {
			if _, metainfoErr := os.Lstat(repository.MetainfoPath(gid)); metainfoErr == nil {
				return app.setActivityWithoutLock(ctx, repository, job, true)
			} else if !errors.Is(metainfoErr, os.ErrNotExist) {
				return metainfoErr
			}
			job.ActivityIntent = jobs.ActivityStopped
			_, err = repository.SaveCAS(job, token)
			return err
		}
		return nil
	}
	if job.Phase != jobs.PhasePending {
		return errors.New("Retry is not applicable to this managed phase")
	}
	current, err := state.Load(app.options.Paths.StateFile)
	if err != nil {
		return err
	}
	rpc, ok := app.options.RPC.(managedRPC)
	if !ok {
		return errors.New("configured RPC does not support managed Retry")
	}
	native, statusErr := rpc.LifecycleStatus(ctx, current, gid)
	workDir := jobs.WorkDir(scope, gid)
	if statusErr == nil {
		if native.GID != gid || filepath.Clean(native.Dir) != filepath.Clean(workDir) {
			return errors.New("ManagedIdentityConflict: live GID points outside its work directory")
		}
		job.Phase, job.ProblemCode = jobs.PhaseStaged, ""
		_, err = repository.SaveCAS(job, token)
		return err
	}
	if !aria2.IsNotFound(statusErr) {
		return statusErr
	}
	entries, err := os.ReadDir(workDir)
	if err != nil || len(entries) != 0 {
		return errors.New("RestartStateMissing: pending Add cannot be retried beside staged artifacts")
	}
	options := aria2.AddOptions{Dir: workDir, GID: gid, Managed: true}
	if strings.HasPrefix(job.Source, "magnet:") {
		options.MetadataOnly, options.SaveMetadata = true, true
	}
	added, err := rpc.AddURI(ctx, current, job.Source, options)
	if err != nil {
		return err
	}
	if added != gid {
		return errors.New("ManagedIdentityConflict: Retry changed GID")
	}
	job.Phase, job.ProblemCode = jobs.PhaseStaged, ""
	_, err = repository.SaveCAS(job, token)
	return err
}

func (app *App) retryStagedWithoutLock(ctx context.Context, repository *jobs.Repository, job jobs.Job, token jobs.Token, scope jobs.StorageScope) error {
	current, err := state.Load(app.options.Paths.StateFile)
	if err != nil {
		return err
	}
	rpc, ok := app.options.RPC.(managedRPC)
	if !ok {
		return errors.New("configured RPC does not support staged Retry")
	}
	workDir := jobs.WorkDir(scope, job.ID)
	native, statusErr := rpc.LifecycleStatus(ctx, current, job.ID)
	if statusErr == nil {
		if native.GID != job.ID || filepath.Clean(native.Dir) != filepath.Clean(workDir) {
			return errors.New("ManagedIdentityConflict: live staged GID points outside its work directory")
		}
		if native.Status == "error" || native.Status == "removed" || native.Status == "complete" {
			return errors.New("RestartStateMissing: staged GID is not resumable in its observed state")
		}
		if job.ActivityIntent == jobs.ActivityStopped && native.Status != "paused" {
			if err := rpc.Pause(ctx, current, job.ID); err != nil {
				return err
			}
		} else if job.ActivityIntent == jobs.ActivityRunning && native.Status == "paused" {
			if err := rpc.Resume(ctx, current, job.ID); err != nil {
				return err
			}
		}
	} else if aria2.IsNotFound(statusErr) {
		fact := inspectStartupFact(repository, job, scope, true)
		options := aria2.AddOptions{Dir: workDir, GID: job.ID, Managed: true, Pause: job.ActivityIntent == jobs.ActivityStopped}
		var added string
		var addErr error
		if fact.Torrent && fact.HasMetainfo {
			metainfo, readErr := readValidatedMetainfo(repository, job.ID)
			if readErr != nil {
				return persistJobProblem(repository, job, token, "RestartStateMissing", readErr)
			}
			added, addErr = rpc.AddTorrent(ctx, current, metainfo, options)
		} else if fact.WorkEmpty && completeSubmittedSource(job.Source) {
			if strings.HasPrefix(job.Source, "magnet:") {
				options.MetadataOnly, options.SaveMetadata = true, true
			}
			added, addErr = rpc.AddURI(ctx, current, job.Source, options)
		} else {
			cause := errors.New("RestartStateMissing: staged artifacts require the preserved native session block and a managed restart")
			return persistJobProblem(repository, job, token, "RestartStateMissing", cause)
		}
		if err := confirmManagedAdd(ctx, rpc, current, job.ID, workDir, added, addErr); err != nil {
			return persistJobProblem(repository, job, token, "AddFailed", err)
		}
	} else {
		return statusErr
	}
	job.ProblemCode = ""
	token, err = repository.SaveCAS(job, token)
	if err != nil {
		return err
	}
	if err := rpc.SaveSession(ctx, current); err != nil {
		return persistJobProblem(repository, job, token, "RestartCheckpointFailed", err)
	}
	return nil
}

func detachManagedNative(ctx context.Context, rpc managedRPC, current state.State, gid string) error {
	native, err := rpc.LifecycleStatus(ctx, current, gid)
	if aria2.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if native.GID != gid {
		return errors.New("ManagedIdentityConflict: lifecycle status returned a different GID")
	}
	switch native.Status {
	case "active", "waiting", "paused":
		if err := rpc.ForceRemove(ctx, current, gid); err != nil && !aria2.IsNotFound(err) {
			return err
		}
	case "complete", "error", "removed":
		// Completed/error results are already inactive and reject forceRemove.
	default:
		return fmt.Errorf("cannot detach managed GID from native status %q", native.Status)
	}
	if err := rpc.RemoveDownloadResult(ctx, current, gid); err != nil && !aria2.IsNotFound(err) {
		return err
	}
	if remaining, statusErr := rpc.LifecycleStatus(ctx, current, gid); statusErr == nil && remaining.GID != "" {
		return errors.New("managed GID remains present after detach")
	} else if statusErr != nil && !aria2.IsNotFound(statusErr) {
		return statusErr
	}
	return nil
}

func (app *App) setActivityWithoutLock(ctx context.Context, repository *jobs.Repository, job jobs.Job, running bool) error {
	// Retry already owns the job lock. Only the published-seed branch is needed.
	if !running {
		return nil
	}
	return app.startFinalSeedWithoutLock(ctx, repository, job)
}

func (app *App) startFinalSeedWithoutLock(ctx context.Context, repository *jobs.Repository, job jobs.Job) error {
	current, err := state.Load(app.options.Paths.StateFile)
	if err != nil {
		return err
	}
	rpc, ok := app.options.RPC.(managedRPC)
	if !ok {
		return errors.New("configured RPC does not support managed activity")
	}
	destination, identity, err := publication.ValidatePayloadRoot(job.TargetDir, job.PayloadRoot)
	if err != nil || identity.MountID != job.PayloadIdentity.MountID ||
		(job.PayloadIdentity.ReliableAcrossRename && identity.ObjectID != job.PayloadIdentity.ObjectID) {
		return app.persistCurrentProblem(repository, job.ID, "FinalSeedPathMismatch",
			errors.Join(errors.New("published payload is missing or its identity changed"), err))
	}
	metainfo, err := readValidatedMetainfo(repository, job.ID)
	if err != nil {
		return app.persistCurrentProblem(repository, job.ID, "FinalSeedStartFailed", err)
	}
	value := false
	removeControl := true
	added, addErr := rpc.AddTorrent(ctx, current, metainfo, aria2.AddOptions{Dir: job.TargetDir, GID: job.ID, Managed: true, SeedUnverified: true, CheckIntegrity: &value, ForceSave: &value, RemoveControlFile: &removeControl})
	if err := confirmManagedAdd(ctx, rpc, current, job.ID, job.TargetDir, added, addErr); err != nil {
		return app.persistCurrentProblem(repository, job.ID, "FinalSeedStartFailed", err)
	}
	seed, statusErr := rpc.LifecycleStatus(ctx, current, job.ID)
	if statusErr != nil || !publishedFilesMatch(seed.Files, destination) {
		return app.persistCurrentProblem(repository, job.ID, "FinalSeedPathMismatch",
			errors.Join(errors.New("final seed files do not match the published payload"), statusErr))
	}
	return nil
}

func (app *App) persistCurrentProblem(repository *jobs.Repository, gid, code string, cause error) error {
	currentJob, token, err := repository.Load(gid)
	if err != nil {
		return errors.Join(cause, err)
	}
	return persistJobProblem(repository, currentJob, token, code, cause)
}

type AddRequest struct {
	Source    string
	TargetDir string
}

type TaskRef struct {
	GID string
}

type ManagedAddResult struct {
	Task    TaskRef
	Warning error
}

func (app *App) AddManaged(ctx context.Context, request AddRequest) (ManagedAddResult, error) {
	if request.TargetDir == "" {
		request.TargetDir = app.defaultDownloadDir()
	}
	if !strings.HasPrefix(request.Source, "http://") && !strings.HasPrefix(request.Source, "https://") && !strings.HasPrefix(request.Source, "magnet:") {
		return ManagedAddResult{}, errors.New("managed source must be HTTP(S) or magnet")
	}
	current, err := state.Load(app.options.Paths.StateFile)
	if err != nil {
		return ManagedAddResult{}, err
	}
	if current.RuntimeSchemaVersion != 2 {
		return ManagedAddResult{}, errors.New("UpgradeRequired: install managed runtime v2 before adding tasks")
	}
	rpc, ok := app.options.RPC.(managedRPC)
	if !ok {
		return ManagedAddResult{}, errors.New("configured RPC does not support managed lifecycle")
	}
	target, err := publication.InspectTarget(request.TargetDir)
	if err != nil {
		return ManagedAddResult{}, err
	}
	repository := jobs.New(app.options.Paths.StateDir)
	scope, err := ensureStorageScope(repository, target)
	if err != nil {
		return ManagedAddResult{}, err
	}
	id, err := randomID()
	if err != nil {
		return ManagedAddResult{}, err
	}
	workDir := jobs.WorkDir(scope, id)
	if err := os.Mkdir(workDir, 0o700); err != nil {
		return ManagedAddResult{}, err
	}
	job := jobs.Job{ID: id, Source: request.Source, TargetDir: target.Path, TargetIdentity: jobIdentity(target.Identity), StorageID: scope.ID, Phase: jobs.PhasePending, ActivityIntent: jobs.ActivityRunning}
	token, err := repository.Create(job)
	if err != nil {
		return ManagedAddResult{}, errors.Join(err, os.Remove(workDir))
	}
	options := aria2.AddOptions{Dir: workDir, GID: id, Managed: true}
	if strings.HasPrefix(request.Source, "magnet:") {
		options.MetadataOnly = true
		options.SaveMetadata = true
	}
	confirmedGID, addErr := rpc.AddURI(ctx, current, request.Source, options)
	if addErr != nil {
		return ManagedAddResult{}, persistJobProblem(repository, job, token, "AddFailed", addErr)
	}
	if confirmedGID != id {
		conflict := fmt.Errorf("managed GID mismatch: requested %s, got %s", id, confirmedGID)
		return ManagedAddResult{}, persistJobProblem(repository, job, token, "ManagedIdentityConflict", conflict)
	}
	job.Phase = jobs.PhaseStaged
	job.ProblemCode = ""
	if _, err := repository.SaveCAS(job, token); err != nil {
		return ManagedAddResult{}, err
	}
	result := ManagedAddResult{Task: TaskRef{GID: id}}
	if err := rpc.SaveSession(ctx, current); err != nil {
		result.Warning = fmt.Errorf("RestartCheckpointFailed: %w", err)
	}
	if err := app.recordDir(target.Path); result.Warning == nil && err != nil {
		result.Warning = err
	}
	return result, nil
}

func ensureStorageScope(repository *jobs.Repository, target publication.Target) (jobs.StorageScope, error) {
	scopes, err := repository.ScanStorages()
	if err != nil {
		return jobs.StorageScope{}, err
	}
	mountPoint := filepath.Clean(target.MountPoint)
	anchor := mountPoint
	rootWritable := unix.Access(mountPoint, unix.W_OK) == nil
	for _, scope := range scopes {
		if filepath.Clean(scope.MountPoint) != mountPoint {
			continue
		}
		if rootWritable && filepath.Clean(scope.StagingAnchor) != anchor {
			continue
		}
		stagingRoot := filepath.Join(scope.StagingAnchor, ".aria2s_staging", scope.ID)
		if pathsOverlap(target.Path, stagingRoot) {
			return jobs.StorageScope{}, errors.New("managed target overlaps the registered staging namespace")
		}
		marker, identifyErr := publication.Identify(stagingRoot)
		if identifyErr != nil || !publication.SameObject(marker, publication.Identity{MountID: scope.Marker.MountID, ObjectID: scope.Marker.ObjectID, ReliableAcrossRename: scope.Marker.ReliableAcrossRename}) {
			return jobs.StorageScope{}, errors.New("StorageMismatch: registered staging marker changed")
		}
		return scope, nil
	}
	// New jobs use one canonical mount-root scope when the user can write there.
	// Existing jobs remain pinned to their registered scope through StorageID.
	if !rootWritable {
		anchor = filepath.Dir(target.Path)
	}
	id, err := randomID()
	if err != nil {
		return jobs.StorageScope{}, err
	}
	root := filepath.Join(anchor, ".aria2s_staging", id)
	if pathsOverlap(target.Path, root) {
		return jobs.StorageScope{}, errors.New("managed target overlaps the staging namespace")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return jobs.StorageScope{}, err
	}
	marker, err := publication.Identify(root)
	if err != nil {
		return jobs.StorageScope{}, err
	}
	scope := jobs.StorageScope{ID: id, MountPoint: target.MountPoint, StagingAnchor: anchor, Marker: jobIdentity(marker)}
	if err := repository.SaveStorage(scope); err != nil {
		return jobs.StorageScope{}, errors.Join(err, os.Remove(root))
	}
	return scope, nil
}

func pathsOverlap(left, right string) bool {
	within := func(child, parent string) bool {
		relative, err := filepath.Rel(parent, child)
		return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
	}
	return within(left, right) || within(right, left)
}

func randomID() (string, error) {
	data := make([]byte, 8)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func jobIdentity(identity publication.Identity) jobs.ObjectIdentity {
	return jobs.ObjectIdentity{MountID: identity.MountID, ObjectID: identity.ObjectID, ReliableAcrossRename: identity.ReliableAcrossRename}
}

func publicationIdentity(identity jobs.ObjectIdentity) publication.Identity {
	return publication.Identity{MountID: identity.MountID, ObjectID: identity.ObjectID, ReliableAcrossRename: identity.ReliableAcrossRename}
}

func (app *App) ManagedHook(ctx context.Context, event, gid string) error {
	if err := closerInheritedLock(); err != nil {
		return err
	}
	if !jobs.ValidID(gid) {
		return errors.New("invalid managed hook GID")
	}
	repository := jobs.New(app.options.Paths.StateDir)
	unlock, err := repository.Lock(ctx, gid)
	if err != nil {
		return err
	}
	defer unlock()
	job, token, err := repository.Load(gid)
	if err != nil {
		return err
	}
	current, err := state.Load(app.options.Paths.StateFile)
	if err != nil {
		return err
	}
	rpc, ok := app.options.RPC.(managedRPC)
	if !ok {
		return errors.New("configured RPC does not support managed hooks")
	}
	native, err := rpc.LifecycleStatus(ctx, current, gid)
	if err != nil {
		if job.Phase == jobs.PhasePublished || job.Phase == jobs.PhaseRemoved {
			return nil
		}
		return err
	}
	scope, err := repository.LoadStorage(job.StorageID)
	if err != nil {
		return err
	}
	if !storageMatches(scope, job) {
		return errors.New("StorageMismatch: hook storage identity changed")
	}
	workDir := jobs.WorkDir(scope, gid)
	if native.GID != gid {
		return errors.New("ManagedIdentityConflict: hook status returned a different GID")
	}
	expectedDir := workDir
	if job.Phase == jobs.PhasePublished {
		expectedDir = job.TargetDir
	}
	if filepath.Clean(native.Dir) != filepath.Clean(expectedDir) {
		return errors.New("ManagedIdentityConflict: native GID points outside its managed directory")
	}
	if job.Phase == jobs.PhasePending {
		job.Phase, job.ProblemCode = jobs.PhaseStaged, ""
		token, err = repository.SaveCAS(job, token)
		if err != nil {
			return err
		}
	}
	if event == "on-download-complete" && job.Phase == jobs.PhaseStaged && (isMetadataStatus(native) || native.InfoHash == "") {
		metainfo, descriptor, err := completedDescriptor(workDir, native)
		if err != nil {
			return err
		}
		if descriptor {
			if err := repository.WriteMetainfo(gid, metainfo); err != nil {
				return err
			}
			if err := rpc.RemoveDownloadResult(ctx, current, gid); err != nil && !aria2.IsNotFound(err) {
				return err
			}
			added, addErr := rpc.AddTorrent(ctx, current, metainfo, aria2.AddOptions{Dir: workDir, GID: gid, Managed: true})
			if err := confirmManagedAdd(ctx, rpc, current, gid, workDir, added, addErr); err != nil {
				return fmt.Errorf("descriptor promotion: %w", err)
			}
			return nil
		}
	}
	if event == "on-download-complete" && job.Phase == jobs.PhasePublished && job.ActivityIntent == jobs.ActivityRunning && native.InfoHash != "" {
		job.ActivityIntent = jobs.ActivityStopped
		if _, err := repository.SaveCAS(job, token); err != nil {
			return err
		}
		if err := rpc.RemoveDownloadResult(ctx, current, gid); err != nil && !aria2.IsNotFound(err) {
			return err
		}
		return nil
	}
	if job.Phase != jobs.PhaseStaged {
		return nil
	}
	isTorrent := native.InfoHash != ""
	if isTorrent && event != "on-bt-download-complete" {
		return nil
	}
	if !isTorrent && event != "on-download-complete" {
		return nil
	}
	if isTorrent {
		if native.Status != "active" || !native.Seeder || native.CompletedLength != native.TotalLength {
			return errors.New("publication guard rejected incomplete torrent completion event")
		}
	} else if native.Status != "complete" || native.CompletedLength != native.TotalLength {
		return errors.New("publication guard rejected incomplete HTTP completion event")
	}
	root, err := payloadRoot(workDir, native.Files)
	if err != nil {
		return err
	}
	source, identity, err := publication.ValidatePayloadRoot(workDir, root)
	if err != nil {
		return err
	}
	var metainfo []byte
	if isTorrent {
		metainfo, err = readValidatedMetainfo(repository, gid)
		if err != nil {
			return errors.New("torrent publication requires retained metainfo")
		}
	}
	job.Phase = jobs.PhasePublishing
	job.PayloadRoot = root
	job.PayloadIdentity = jobIdentity(identity)
	payloadLength := native.TotalLength
	job.PayloadLength = &payloadLength
	token, err = repository.SaveCAS(job, token)
	if err != nil {
		return err
	}
	if isTorrent {
		if native.Status == "active" {
			if err := rpc.Pause(ctx, current, gid); err != nil {
				return err
			}
		}
		if err := rpc.ForceRemove(ctx, current, gid); err != nil && !aria2.IsNotFound(err) {
			return err
		}
	}
	if err := rpc.RemoveDownloadResult(ctx, current, gid); err != nil && !aria2.IsNotFound(err) {
		return err
	}
	destination := filepath.Join(job.TargetDir, root)
	currentIdentity, err := publication.Identify(source)
	if err != nil || !publication.SameObject(identity, currentIdentity) {
		return errors.New("PublicationPayloadMismatch: payload identity changed before publication")
	}
	move, err := publication.MoveExpected(source, destination, identity, publicationIdentity(job.TargetIdentity))
	if err != nil {
		return persistJobProblem(repository, job, token, publicationProblem(err), err)
	}
	job.Phase = jobs.PhasePublished
	job.ProblemCode = ""
	if !isTorrent {
		job.ActivityIntent = jobs.ActivityStopped
	}
	if move.DirectorySyncUnsupported {
		job.ProblemCode = "PowerLossDurabilityUnavailable"
	}
	token, err = repository.SaveCAS(job, token)
	if err != nil {
		return err
	}
	if isTorrent && job.ActivityIntent == jobs.ActivityRunning {
		value := false
		removeControl := true
		added, addErr := rpc.AddTorrent(ctx, current, metainfo, aria2.AddOptions{Dir: job.TargetDir, GID: gid, Managed: true, SeedUnverified: true, CheckIntegrity: &value, ForceSave: &value, RemoveControlFile: &removeControl})
		if err := confirmManagedAdd(ctx, rpc, current, gid, job.TargetDir, added, addErr); err != nil {
			return persistJobProblem(repository, job, token, "FinalSeedStartFailed", fmt.Errorf("final seed: %w", err))
		}
		seed, statusErr := rpc.LifecycleStatus(ctx, current, gid)
		if statusErr != nil || !publishedFilesMatch(seed.Files, destination) {
			conflict := errors.New("ManagedIdentityConflict: final seed files do not match the published payload")
			return persistJobProblem(repository, job, token, "FinalSeedPathMismatch", errors.Join(conflict, statusErr))
		}
	}
	if cleanupErr := cleanupPublishedWorkDir(workDir, root); cleanupErr != nil {
		return persistJobProblem(repository, job, token, "CleanupFailed", cleanupErr)
	}
	return nil
}

func publicationProblem(err error) string {
	switch {
	case errors.Is(err, publication.ErrConflict):
		return "PublicationConflict"
	case errors.Is(err, publication.ErrCrossDevice):
		return "PublicationUnsupported"
	default:
		return "PublicationStateUncertain"
	}
}

func persistJobProblem(repository *jobs.Repository, job jobs.Job, token jobs.Token, code string, cause error) error {
	job.ProblemCode = code
	if _, err := repository.SaveCAS(job, token); err != nil {
		return errors.Join(cause, fmt.Errorf("persist %s: %w", code, err))
	}
	return cause
}

func confirmManagedAdd(ctx context.Context, rpc managedRPC, current state.State, gid, expectedDir, added string, addErr error) error {
	if addErr == nil && added != gid {
		return errors.New("ManagedIdentityConflict: Add changed GID")
	}
	if addErr != nil && !errors.Is(addErr, aria2.ErrOutcomeUnknown) {
		return addErr
	}
	native, statusErr := rpc.LifecycleStatus(ctx, current, gid)
	if statusErr != nil {
		if addErr != nil {
			return addErr
		}
		return statusErr
	}
	if native.GID != gid || filepath.Clean(native.Dir) != filepath.Clean(expectedDir) {
		return errors.New("ManagedIdentityConflict: confirmed Add has unexpected ownership")
	}
	return nil
}

func publishedFilesMatch(files []aria2.DownloadFile, publishedRoot string) bool {
	if len(files) == 0 {
		return false
	}
	root := filepath.Clean(publishedRoot)
	for _, file := range files {
		path := filepath.Clean(file.Path)
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return false
		}
	}
	return true
}

var closerInheritedLock = managedruntime.CloseInheritedLock

func isMetadataStatus(native aria2.LifecycleStatus) bool {
	if native.InfoHash == "" {
		return false
	}
	for _, file := range native.Files {
		if strings.HasPrefix(file.Path, "[METADATA]") {
			return true
		}
	}
	return false
}

func findResolvedMetainfo(workDir, infoHash string) ([]byte, error) {
	decoded, decodeErr := hex.DecodeString(infoHash)
	if decodeErr != nil || len(decoded) != 20 {
		return nil, errors.New("descriptor has no info hash")
	}
	path := filepath.Join(workDir, strings.ToLower(infoHash)+".torrent")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	actual, err := aria2.ValidateMetainfo(data)
	if err != nil || !strings.EqualFold(actual, infoHash) {
		return nil, errors.New("resolved torrent metainfo failed validation")
	}
	return data, nil
}

func readValidatedMetainfo(repository *jobs.Repository, gid string) ([]byte, error) {
	metainfo, err := repository.ReadMetainfo(gid)
	if err != nil {
		return nil, err
	}
	if _, err := aria2.ValidateMetainfo(metainfo); err != nil {
		return nil, fmt.Errorf("invalid retained metainfo: %w", err)
	}
	return metainfo, nil
}

func completedDescriptor(workDir string, native aria2.LifecycleStatus) ([]byte, bool, error) {
	if isMetadataStatus(native) {
		data, err := findResolvedMetainfo(workDir, native.InfoHash)
		return data, true, err
	}
	if native.Status != "complete" || len(native.Files) != 1 {
		return nil, false, nil
	}
	relative, err := filepath.Rel(workDir, native.Files[0].Path)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.Base(relative) != relative {
		return nil, false, errors.New("descriptor path escapes managed work directory")
	}
	info, err := os.Lstat(native.Files[0].Path)
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return nil, false, nil
	}
	data, err := os.ReadFile(native.Files[0].Path)
	if err != nil {
		return nil, false, err
	}
	if _, err := aria2.ValidateMetainfo(data); err != nil {
		return nil, false, nil
	}
	return data, true, nil
}

func payloadRoot(workDir string, files []aria2.DownloadFile) (string, error) {
	if len(files) == 0 {
		return "", errors.New("native task has no payload files")
	}
	var root string
	for _, file := range files {
		relative, err := filepath.Rel(workDir, file.Path)
		if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", errors.New("ManagedIdentityConflict: native path escapes work directory")
		}
		first := strings.Split(filepath.Clean(relative), string(filepath.Separator))[0]
		if root == "" {
			root = first
		} else if root != first {
			return "", errors.New("native files do not share one publishable root")
		}
	}
	return root, nil
}

func cleanupPublishedWorkDir(workDir, root string) error {
	entries, err := os.ReadDir(workDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !isPublishedCleanupArtifact(entry.Name(), root) {
			return fmt.Errorf("unexpected staging artifact remains after publication: %s", entry.Name())
		}
		if err := os.Remove(filepath.Join(workDir, entry.Name())); err != nil {
			return err
		}
	}
	if err := os.Remove(workDir); err != nil {
		return err
	}
	return atomicfile.SyncDirectory(filepath.Dir(workDir))
}

func isPublishedCleanupArtifact(name, root string) bool {
	if name == root+".aria2" || strings.HasSuffix(name, ".torrent") {
		return true
	}
	// macOS stores extended attributes in AppleDouble companions on filesystems
	// such as exFAT. Only companions of already-managed transients are safe to
	// remove; unrelated sidecars must retain the same cleanup guard as user data.
	companion, found := strings.CutPrefix(name, "._")
	return found && (companion == root+".aria2" || strings.HasSuffix(companion, ".torrent"))
}

func removeWorkDir(workDir string) error {
	if err := os.RemoveAll(workDir); err != nil {
		return err
	}
	return atomicfile.SyncDirectory(filepath.Dir(workDir))
}
