// Package app owns crash-safe convergence between managed manifests, aria2,
// and publication. ReconcileJob owns environment-driven convergence; explicit
// commands prepare their durable intent or recovery facts before entering it.
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
	"time"

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

type ReconcileMode string

const (
	ReconcileLive    ReconcileMode = "live"
	ReconcileStartup ReconcileMode = "startup"
)

// ReconcileInput contains only environment-specific native observations. The
// lifecycle policy is shared between live commands/hooks and managed startup.
type ReconcileInput struct {
	Mode           ReconcileMode
	SavedBlock     *aria2.SessionBlock
	SavedDuplicate bool
	ExpectedGID    string
}

type ReconcileResult struct {
	StartupBlock *aria2.SessionBlock
	Warning      error
}

type liveEnvironment struct {
	rpc     managedRPC
	current state.State
}

func (app *App) ReconcileJob(ctx context.Context, jobID string, input ReconcileInput) (ReconcileResult, error) {
	repository := jobs.New(app.options.Paths.StateDir)
	unlock, err := repository.Lock(ctx, jobID)
	if err != nil {
		return ReconcileResult{}, err
	}
	defer unlock()
	job, token, err := repository.Load(jobID)
	if err != nil {
		return ReconcileResult{}, err
	}
	if input.ExpectedGID != "" && (job.Execution == nil || job.Execution.GID != input.ExpectedGID) {
		return ReconcileResult{}, nil // stale hook
	}
	if err := ensureExecutionBindingUnique(repository, job, token); err != nil {
		return ReconcileResult{}, err
	}
	if input.Mode == "" {
		input.Mode = ReconcileLive
	}
	if input.Mode == ReconcileStartup {
		return app.reconcileStartupLocked(ctx, repository, job, token, input.SavedBlock, input.SavedDuplicate)
	}
	env, err := app.liveEnvironment()
	if err != nil {
		return ReconcileResult{}, err
	}
	return app.reconcileLiveLocked(ctx, repository, env, job, token)
}

func (app *App) liveEnvironment() (liveEnvironment, error) {
	current, err := state.Load(app.options.Paths.StateFile)
	if err != nil {
		return liveEnvironment{}, err
	}
	rpc, ok := app.options.RPC.(managedRPC)
	if !ok {
		return liveEnvironment{}, errors.New("configured RPC does not support managed reconciliation")
	}
	return liveEnvironment{rpc: rpc, current: current}, nil
}

func (app *App) reconcileLiveLocked(ctx context.Context, repository *jobs.Repository, env liveEnvironment, job jobs.Job, token jobs.Token) (ReconcileResult, error) {
	if job.Removed {
		return app.reconcileRemovedLive(ctx, repository, env, job, token)
	}
	scope, err := repository.LoadStorage(job.StorageID)
	if err != nil || !storageIdentityMatches(scope, job) {
		return ReconcileResult{}, persistIssue(repository, job, token, "StorageOffline", errors.Join(errors.New("registered storage is unavailable or mismatched"), err))
	}
	if job.Payload.Location == jobs.PayloadStaging && job.Payload.FinalRoot != "" {
		if job.Execution != nil {
			if err := validateAndDetach(ctx, env, job, scope); err != nil {
				return ReconcileResult{}, persistIssue(repository, job, token, issueCode(err, "PublicationRecoveryRequired"), err)
			}
			job.Execution = nil
			token, err = repository.SaveCAS(job, token)
			if err != nil {
				return ReconcileResult{}, err
			}
		}
		job, token, err = reconcilePreparedPublication(ctx, repository, job, token, scope)
		if err != nil || job.Payload.Location != jobs.PayloadPublished {
			return ReconcileResult{}, err
		}
	}
	if job.Payload.Location == jobs.PayloadPublished {
		return app.reconcilePublishedLive(ctx, repository, env, job, token, scope)
	}
	return app.reconcileStagedLive(ctx, repository, env, job, token, scope)
}

func (app *App) reconcileStagedLive(ctx context.Context, repository *jobs.Repository, env liveEnvironment, job jobs.Job, token jobs.Token, scope jobs.StorageScope) (ReconcileResult, error) {
	workDir := jobs.WorkDir(scope, job.ID)
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return ReconcileResult{}, err
	}
	if job.Execution == nil {
		gid, err := randomExecutionID(job.ID)
		if err != nil {
			return ReconcileResult{}, err
		}
		job.Execution = &jobs.ExecutionBinding{GID: gid}
		token, err = repository.SaveCAS(job, token) // binding always precedes Add
		if err != nil {
			return ReconcileResult{}, err
		}
	}
	gid := job.Execution.GID
	native, statusErr := env.rpc.LifecycleStatus(ctx, env.current, gid)
	if statusErr == nil && native.GID == "" {
		statusErr = &aria2.RPCError{Method: "aria2.tellStatus", Code: 1, Message: "not found"}
	}
	if statusErr == nil {
		if err := validateNative(job, scope, native); err != nil {
			return ReconcileResult{}, persistIssue(repository, job, token, "ManagedIdentityConflict", err)
		}
		if native.Status == "error" || native.Status == "removed" {
			if err := detachManagedNative(ctx, env.rpc, env.current, gid); err != nil {
				return ReconcileResult{}, err
			}
			job.Execution = nil
			next, saveErr := repository.SaveCAS(job, token)
			if saveErr != nil {
				return ReconcileResult{}, saveErr
			}
			return app.reconcileStagedLive(ctx, repository, env, job, next, scope)
		}
		if descriptor, promoted, err := completedDescriptor(workDir, native); err != nil {
			return ReconcileResult{}, err
		} else if promoted {
			return app.promoteDescriptor(ctx, repository, env, job, token, scope, descriptor)
		}
		if transferComplete(native) {
			return app.prepareAndPublish(ctx, repository, env, job, token, scope, native)
		}
		if err := convergeActivity(ctx, env, gid, native.Status, job.ActivityIntent); err != nil {
			return ReconcileResult{}, err
		}
		return checkpointAndClearIssue(ctx, repository, env, job, token)
	}
	if !aria2.IsNotFound(statusErr) {
		return ReconcileResult{}, statusErr
	}
	// A previous definite/unknown Add failure is authoritatively absent. Retire
	// that attempt and allocate a fresh execution before retrying.
	if job.Issue != nil || gid == job.ID {
		job.Execution = nil
		token, statusErr = repository.SaveCAS(job, token)
		if statusErr != nil {
			return ReconcileResult{}, statusErr
		}
		gid, statusErr = randomExecutionID(job.ID)
		if statusErr != nil {
			return ReconcileResult{}, statusErr
		}
		job.Execution = &jobs.ExecutionBinding{GID: gid}
		token, statusErr = repository.SaveCAS(job, token)
		if statusErr != nil {
			return ReconcileResult{}, statusErr
		}
	}
	fact := inspectStartupFact(repository, job, scope, true)
	options := stagedAddOptions(job, workDir, fact)
	var added string
	var addErr error
	if fact.Torrent && fact.HasMetainfo {
		metainfo, readErr := readValidatedMetainfo(repository, job.ID)
		if readErr != nil {
			return ReconcileResult{}, persistIssue(repository, job, token, "RestartStateMissing", readErr)
		}
		added, addErr = env.rpc.AddTorrent(ctx, env.current, metainfo, options)
	} else if fact.WorkEmpty && completeSubmittedSource(job.Source) {
		if strings.HasPrefix(job.Source, "magnet:") {
			options.MetadataOnly, options.SaveMetadata = true, true
		}
		added, addErr = env.rpc.AddURI(ctx, env.current, job.Source, options)
	} else {
		return ReconcileResult{}, persistIssue(repository, job, token, "RestartStateMissing", errors.New("staged artifacts have no safe restart state"))
	}
	if err := confirmManagedAdd(ctx, env.rpc, env.current, gid, workDir, added, addErr); err != nil {
		return ReconcileResult{}, persistIssue(repository, job, token, "AddFailed", err)
	}
	return checkpointAndClearIssue(ctx, repository, env, job, token)
}

func (app *App) promoteDescriptor(ctx context.Context, repository *jobs.Repository, env liveEnvironment, job jobs.Job, token jobs.Token, scope jobs.StorageScope, metainfo []byte) (ReconcileResult, error) {
	if err := repository.WriteMetainfo(job.ID, metainfo); err != nil {
		return ReconcileResult{}, err
	}
	if err := validateAndDetach(ctx, env, job, scope); err != nil {
		return ReconcileResult{}, err
	}
	job.Execution = nil
	job.Issue = nil
	token, err := repository.SaveCAS(job, token)
	if err != nil {
		return ReconcileResult{}, err
	}
	return app.reconcileStagedLive(ctx, repository, env, job, token, scope)
}

func (app *App) prepareAndPublish(ctx context.Context, repository *jobs.Repository, env liveEnvironment, job jobs.Job, token jobs.Token, scope jobs.StorageScope, native aria2.LifecycleStatus) (ReconcileResult, error) {
	workDir := jobs.WorkDir(scope, job.ID)
	root, err := payloadRoot(workDir, native.Files)
	if err != nil {
		return ReconcileResult{}, err
	}
	source, identity, err := publication.ValidatePayloadRoot(workDir, root)
	if err != nil {
		return ReconcileResult{}, err
	}
	if native.InfoHash != "" {
		if _, err := readValidatedMetainfo(repository, job.ID); err != nil {
			return ReconcileResult{}, persistIssue(repository, job, token, "RestartStateMissing", errors.New("torrent publication requires retained metainfo"))
		}
	}
	unlockPublication, err := repository.LockPublication(ctx)
	if err != nil {
		return ReconcileResult{}, err
	}
	locked := true
	defer func() {
		if locked {
			_ = unlockPublication()
		}
	}()
	finalRoot, err := publication.AvailableRoot(source, job.TargetDir, root)
	if err != nil {
		return ReconcileResult{}, persistIssue(repository, job, token, publicationProblem(err), err)
	}
	length := native.TotalLength
	job.Payload = jobs.PayloadState{Location: jobs.PayloadStaging, Root: root, FinalRoot: finalRoot, Identity: jobIdentity(identity), Length: &length}
	token, err = repository.SaveCAS(job, token) // publication intent precedes detach/move
	if err != nil {
		return ReconcileResult{}, err
	}
	if err := validateAndDetach(ctx, env, job, scope); err != nil {
		return ReconcileResult{}, persistIssue(repository, job, token, "PublicationRecoveryRequired", err)
	}
	job.Execution = nil
	token, err = repository.SaveCAS(job, token)
	if err != nil {
		return ReconcileResult{}, err
	}
	job, token, err = reconcilePreparedPublicationUnderLock(repository, job, token, scope)
	if err != nil || job.Payload.Location != jobs.PayloadPublished {
		return ReconcileResult{}, err
	}
	if err := unlockPublication(); err != nil {
		return ReconcileResult{}, err
	}
	locked = false
	return app.reconcilePublishedLive(ctx, repository, env, job, token, scope)
}

func (app *App) reconcilePublishedLive(ctx context.Context, repository *jobs.Repository, env liveEnvironment, job jobs.Job, token jobs.Token, scope jobs.StorageScope) (ReconcileResult, error) {
	destination, identity, err := publication.ValidatePayloadRoot(job.TargetDir, job.FinalRoot())
	if err != nil || identity.MountID != job.Payload.Identity.MountID || (job.Payload.Identity.ReliableAcrossRename && identity.ObjectID != job.Payload.Identity.ObjectID) {
		return ReconcileResult{}, persistIssue(repository, job, token, "FinalSeedPathMismatch", errors.Join(errors.New("published payload identity changed"), err))
	}
	metainfo, metaErr := readValidatedMetainfo(repository, job.ID)
	if job.PublicationRenamed() || errors.Is(metaErr, os.ErrNotExist) {
		job.ActivityIntent = jobs.ActivityStopped
	}
	if job.Execution != nil {
		native, statusErr := env.rpc.LifecycleStatus(ctx, env.current, job.Execution.GID)
		if statusErr == nil && native.GID == "" {
			statusErr = &aria2.RPCError{Method: "aria2.tellStatus", Code: 1, Message: "not found"}
		}
		if aria2.IsNotFound(statusErr) {
			job.Execution = nil
			token, err = repository.SaveCAS(job, token)
			if err != nil {
				return ReconcileResult{}, err
			}
		} else if statusErr != nil {
			return ReconcileResult{}, statusErr
		} else {
			if native.GID != job.Execution.GID || filepath.Clean(native.Dir) != filepath.Clean(job.TargetDir) || !publishedFilesMatch(native.Files, destination) {
				return ReconcileResult{}, persistIssue(repository, job, token, "FinalSeedPathMismatch", errors.New("native seed does not match published payload"))
			}
			if native.Status == "complete" {
				job.ActivityIntent = jobs.ActivityStopped
				if err := detachManagedNative(ctx, env.rpc, env.current, native.GID); err != nil {
					return ReconcileResult{}, err
				}
				job.Execution = nil
				token, err = repository.SaveCAS(job, token)
				if err != nil {
					return ReconcileResult{}, err
				}
			} else if native.Status == "error" || native.Status == "removed" {
				if err := detachManagedNative(ctx, env.rpc, env.current, native.GID); err != nil {
					return ReconcileResult{}, err
				}
				job.Execution = nil
				token, err = repository.SaveCAS(job, token)
				if err != nil {
					return ReconcileResult{}, err
				}
			} else {
				if err := convergeActivity(ctx, env, native.GID, native.Status, job.ActivityIntent); err != nil {
					return ReconcileResult{}, persistIssue(repository, job, token, "FinalSeedStartFailed", err)
				}
				return checkpointAndClearIssue(ctx, repository, env, job, token)
			}
		}
	}
	if job.ActivityIntent == jobs.ActivityRunning && !job.PublicationRenamed() {
		if metaErr != nil {
			return ReconcileResult{}, persistIssue(repository, job, token, "FinalSeedStartFailed", metaErr)
		}
		gid, err := randomExecutionID(job.ID)
		if err != nil {
			return ReconcileResult{}, err
		}
		job.Execution = &jobs.ExecutionBinding{GID: gid}
		token, err = repository.SaveCAS(job, token)
		if err != nil {
			return ReconcileResult{}, err
		}
		check := false
		removeControl := true
		added, addErr := env.rpc.AddTorrent(ctx, env.current, metainfo, aria2.AddOptions{Dir: job.TargetDir, GID: gid, Managed: true, SeedUnverified: true, CheckIntegrity: &check, ForceSave: &check, RemoveControlFile: &removeControl})
		if err := confirmManagedAdd(ctx, env.rpc, env.current, gid, job.TargetDir, added, addErr); err != nil {
			return ReconcileResult{}, persistIssue(repository, job, token, "FinalSeedStartFailed", err)
		}
		seed, err := env.rpc.LifecycleStatus(ctx, env.current, gid)
		if err != nil || !publishedFilesMatch(seed.Files, destination) {
			return ReconcileResult{}, persistIssue(repository, job, token, "FinalSeedPathMismatch", errors.Join(errors.New("final seed files do not match payload"), err))
		}
	}
	result, err := checkpointAndClearIssue(ctx, repository, env, job, token)
	if err != nil {
		return ReconcileResult{}, err
	}
	if cleanupErr := cleanupPublishedWorkDir(jobs.WorkDir(scope, job.ID), job.Payload.Root); cleanupErr != nil {
		// Cleanup is deliberately best effort and cannot invalidate publication.
		result.Warning = errors.Join(result.Warning, cleanupErr)
	}
	return result, nil
}

func (app *App) reconcileRemovedLive(ctx context.Context, repository *jobs.Repository, env liveEnvironment, job jobs.Job, token jobs.Token) (ReconcileResult, error) {
	if job.Execution != nil {
		scope, err := repository.LoadStorage(job.StorageID)
		if err != nil {
			return ReconcileResult{}, persistIssue(repository, job, token, "StorageOffline", err)
		}
		if err := validateAndDetach(ctx, env, job, scope); err != nil {
			return ReconcileResult{}, err
		}
		job.Execution = nil
		token, err = repository.SaveCAS(job, token)
		if err != nil {
			return ReconcileResult{}, err
		}
	}
	if job.Payload.Location == jobs.PayloadStaging {
		scope, err := repository.LoadStorage(job.StorageID)
		if err != nil || !stagingIdentityMatches(scope) {
			return ReconcileResult{}, persistIssue(repository, job, token, "StorageOffline", errors.Join(errors.New("cannot clean changed storage"), err))
		}
		if err := removeWorkDir(jobs.WorkDir(scope, job.ID)); err != nil {
			return ReconcileResult{}, persistIssue(repository, job, token, "CleanupFailed", err)
		}
	}
	job.Issue = nil
	_, err := repository.SaveCAS(job, token)
	return ReconcileResult{}, err
}

func (app *App) reconcileStartupLocked(ctx context.Context, repository *jobs.Repository, job jobs.Job, token jobs.Token, saved *aria2.SessionBlock, duplicate bool) (ReconcileResult, error) {
	if job.Removed {
		if job.Execution != nil { // exclusive startup lease + omission proves retirement
			job.Execution = nil
			_, err := repository.SaveCAS(job, token)
			return ReconcileResult{}, err
		}
		return ReconcileResult{}, nil
	}
	if duplicate {
		return ReconcileResult{}, persistIssue(repository, job, token, "RestartStateMissing", errors.New("duplicate native session blocks for execution GID"))
	}
	if job.LegacyPending() && saved == nil {
		job.Execution = nil
		_, err := repository.SaveCAS(job, token)
		return ReconcileResult{}, err
	}
	if job.Payload.Location == jobs.PayloadStaging && job.Payload.FinalRoot == "" && job.Execution == nil {
		// A pending manifest has no native owner. Startup omission is complete
		// without touching its potentially offline storage.
		return ReconcileResult{}, nil
	}
	scope, err := repository.LoadStorage(job.StorageID)
	if err != nil || !storageIdentityMatches(scope, job) {
		return ReconcileResult{}, persistIssue(repository, job, token, "StorageOffline", errors.Join(errors.New("registered storage unavailable"), err))
	}
	if job.Payload.Location == jobs.PayloadStaging && job.Payload.FinalRoot != "" {
		if job.Execution != nil {
			job.Execution = nil // omitting saved block retires it under startup lease
			token, err = repository.SaveCAS(job, token)
			if err != nil {
				return ReconcileResult{}, err
			}
		}
		job, token, err = reconcilePreparedPublication(ctx, repository, job, token, scope)
		if err != nil || job.Payload.Location != jobs.PayloadPublished {
			return ReconcileResult{}, err
		}
	}
	if job.Payload.Location == jobs.PayloadPublished {
		_, identity, identityErr := publication.ValidatePayloadRoot(job.TargetDir, job.FinalRoot())
		if identityErr != nil || identity.MountID != job.Payload.Identity.MountID ||
			(job.Payload.Identity.ReliableAcrossRename && identity.ObjectID != job.Payload.Identity.ObjectID) {
			return ReconcileResult{}, persistIssue(repository, job, token, "FinalSeedPathMismatch",
				errors.Join(errors.New("published payload identity changed"), identityErr))
		}
		if job.PublicationRenamed() || job.ActivityIntent == jobs.ActivityStopped {
			job.Execution = nil // startup omission is authoritative under the lease
			job.Issue = nil
			_, err := repository.SaveCAS(job, token)
			return ReconcileResult{}, err
		}
		metainfo, err := readValidatedMetainfo(repository, job.ID)
		if err != nil {
			return ReconcileResult{}, persistIssue(repository, job, token, "RestartStateMissing", err)
		}
		_ = metainfo // startup block references the retained file
		gid, err := randomExecutionID(job.ID)
		if err != nil {
			return ReconcileResult{}, err
		}
		job.Execution = &jobs.ExecutionBinding{GID: gid}
		job.Issue = nil
		if _, err := repository.SaveCAS(job, token); err != nil {
			return ReconcileResult{}, err
		}
		block := generatedTorrentBlock(job, repository.MetainfoPath(job.ID), job.TargetDir, false, true)
		return ReconcileResult{StartupBlock: &block}, nil
	}
	fact := inspectStartupFact(repository, job, scope, true)
	workDir := jobs.WorkDir(scope, job.ID)
	if saved != nil && job.Execution != nil {
		if savedGID, ok := saved.Option("gid"); !ok || savedGID != job.Execution.GID {
			return ReconcileResult{}, persistIssue(repository, job, token, "ManagedIdentityConflict", errors.New("saved block GID does not match execution binding"))
		}
		block, problem := normalizeStagedBlock(*saved, job, workDir, fact)
		if problem != "" {
			return ReconcileResult{}, persistIssue(repository, job, token, "RestartStateMissing", errors.New(problem))
		}
		applyMissingControlRecovery(&block, fact)
		job.Issue = nil
		if _, err := repository.SaveCAS(job, token); err != nil {
			return ReconcileResult{}, err
		}
		return ReconcileResult{StartupBlock: &block}, nil
	}
	// No saved owner exists under the startup lease. Retire the old binding and
	// reconstruct, with a fresh GID, only from safe durable inputs.
	job.Execution = nil
	gid, err := randomExecutionID(job.ID)
	if err != nil {
		return ReconcileResult{}, err
	}
	job.Execution = &jobs.ExecutionBinding{GID: gid}
	job.Issue = nil
	if fact.InferredRoot != "" && job.Payload.Root == "" {
		job.Payload.Root = fact.InferredRoot
	}
	token, err = repository.SaveCAS(job, token)
	if err != nil {
		return ReconcileResult{}, err
	}
	var block aria2.SessionBlock
	if fact.Torrent && fact.HasMetainfo {
		block = generatedTorrentBlock(job, fact.MetainfoPath, workDir, job.ActivityIntent == jobs.ActivityStopped, false)
	} else if fact.WorkEmpty && completeSubmittedSource(job.Source) {
		block = aria2.SessionBlock{URI: job.Source}
		applyManagedOptions(&block, job, workDir)
	} else {
		return ReconcileResult{}, persistIssue(repository, job, token, "RestartStateMissing", errors.New("native block is missing beside staged artifacts"))
	}
	applyMissingControlRecovery(&block, fact)
	return ReconcileResult{StartupBlock: &block}, nil
}

func (app *App) SetActivity(ctx context.Context, jobID string, running bool) error {
	repository := jobs.New(app.options.Paths.StateDir)
	unlock, err := repository.Lock(ctx, jobID)
	if err != nil {
		return err
	}
	job, token, err := repository.Load(jobID)
	if err == nil {
		job.ActivityIntent = jobs.ActivityStopped
		if running {
			job.ActivityIntent = jobs.ActivityRunning
		}
		_, err = repository.SaveCAS(job, token)
	}
	if unlockErr := unlock(); err == nil {
		err = unlockErr
	}
	if err != nil {
		return err
	}
	result, err := app.ReconcileJob(ctx, jobID, ReconcileInput{Mode: ReconcileLive})
	return reconcileCommandError(result, err)
}

func (app *App) RemoveManaged(ctx context.Context, jobID string) error {
	repository := jobs.New(app.options.Paths.StateDir)
	unlock, err := repository.Lock(ctx, jobID)
	if err != nil {
		return err
	}
	job, token, err := repository.Load(jobID)
	if err == nil {
		job.Removed, job.ActivityIntent = true, jobs.ActivityStopped
		_, err = repository.SaveCAS(job, token)
	}
	if unlockErr := unlock(); err == nil {
		err = unlockErr
	}
	if err != nil {
		return err
	}
	result, err := app.ReconcileJob(ctx, jobID, ReconcileInput{Mode: ReconcileLive})
	return reconcileCommandError(result, err)
}

// DeleteManaged fully retires a disposable managed task. Removal owns native
// and staging cleanup; Clear then removes the durable manifest projection.
func (app *App) DeleteManaged(ctx context.Context, jobID string) error {
	if err := app.RemoveManaged(ctx, jobID); err != nil {
		return err
	}
	return app.ClearManaged(ctx, jobID)
}

func (app *App) RetryManaged(ctx context.Context, jobID string) error {
	repository := jobs.New(app.options.Paths.StateDir)
	unlock, err := repository.Lock(ctx, jobID)
	if err != nil {
		return err
	}
	defer unlock()

	job, token, err := repository.Load(jobID)
	if err != nil {
		return err
	}
	if err := ensureExecutionBindingUnique(repository, job, token); err != nil {
		return err
	}
	env, err := app.liveEnvironment()
	if err != nil {
		return err
	}

	// Removal cleanup remains authoritative until it succeeds. Only then may
	// Retry revive the task and prepare explicit target recovery.
	if job.Removed {
		if _, err := app.reconcileRemovedLive(ctx, repository, env, job, token); err != nil {
			return err
		}
		job, token, err = repository.Load(jobID)
		if err != nil {
			return err
		}
		job.Removed = false
		job.ActivityIntent = jobs.ActivityRunning
		if job.Payload.Location == jobs.PayloadPublished && job.PublicationRenamed() {
			job.ActivityIntent = jobs.ActivityStopped
		}
		token, err = repository.SaveCAS(job, token)
		if err != nil {
			return err
		}
	}
	job, token, err = adoptRecreatedTarget(repository, job, token)
	if err != nil {
		return err
	}
	result, err := app.reconcileLiveLocked(ctx, repository, env, job, token)
	return reconcileCommandError(result, err)
}

// adoptRecreatedTarget lets an explicit Retry accept a replacement for the
// configured target directory before publication. The registered staging
// marker remains authoritative, and later lifecycle entry points stay strict.
func adoptRecreatedTarget(repository *jobs.Repository, job jobs.Job, token jobs.Token) (jobs.Job, jobs.Token, error) {
	if job.Payload.Location != jobs.PayloadStaging || job.Payload.FinalRoot != "" {
		return job, token, nil
	}
	scope, err := repository.LoadStorage(job.StorageID)
	if err != nil || !stagingIdentityMatches(scope) {
		return job, token, nil
	}
	target, err := retryTarget(scope, job.TargetDir)
	if err != nil {
		return job, token, nil
	}
	if target.Identity.MountID == job.TargetIdentity.MountID && target.Identity.ObjectID == job.TargetIdentity.ObjectID {
		return job, token, nil
	}
	job.TargetIdentity = jobIdentity(target.Identity)
	next, err := repository.SaveCAS(job, token)
	return job, next, err
}

func retryTarget(scope jobs.StorageScope, path string) (publication.Target, error) {
	target, err := publication.InspectTarget(path)
	if err == nil {
		return validateRetryTarget(scope, path, target)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return publication.Target{}, err
	}

	parent, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return publication.Target{}, err
	}
	physicalParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return publication.Target{}, err
	}
	if filepath.Clean(physicalParent) != filepath.Clean(parent) {
		return publication.Target{}, errors.New("target parent is not a physical directory")
	}
	parentIdentity, err := publication.Identify(parent)
	if err != nil || parentIdentity.MountID != scope.Marker.MountID {
		return publication.Target{}, errors.Join(errors.New("target parent is outside the registered storage"), err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		return publication.Target{}, err
	}
	target, err = publication.InspectTarget(path)
	if err == nil {
		target, err = validateRetryTarget(scope, path, target)
	}
	if err != nil {
		return publication.Target{}, errors.Join(err, os.Remove(path))
	}
	return target, nil
}

func validateRetryTarget(scope jobs.StorageScope, path string, target publication.Target) (publication.Target, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return publication.Target{}, err
	}
	if filepath.Clean(target.Path) != filepath.Clean(abs) || target.Identity.MountID != scope.Marker.MountID || filepath.Clean(target.MountPoint) != filepath.Clean(scope.MountPoint) {
		return publication.Target{}, errors.New("target is outside the registered storage or resolves through a symlink")
	}
	return target, nil
}

func (app *App) ClearManaged(ctx context.Context, jobID string) error {
	repository := jobs.New(app.options.Paths.StateDir)
	unlock, err := repository.Lock(ctx, jobID)
	if err != nil {
		return err
	}
	defer unlock()
	job, token, err := repository.Load(jobID)
	if err != nil {
		if repository.Exists(jobID) {
			return errors.New("cannot Clear corrupt managed metadata because its execution binding is unknown")
		}
		return err
	}
	if job.Execution != nil {
		return errors.New("managed execution must retire before Clear")
	}
	if job.Payload.Location == jobs.PayloadStaging && !job.Removed {
		return errors.New("active managed task cannot be cleared")
	}
	return repository.DeleteCAS(jobID, token)
}

type AddRequest struct{ Source, TargetDir string }
type TaskRef struct {
	JobID string
}
type ManagedAddResult struct {
	Task    TaskRef
	Warning error
}

func (app *App) AddManaged(ctx context.Context, request AddRequest) (ManagedAddResult, error) {
	if request.TargetDir == "" {
		request.TargetDir = app.defaultDownloadDir()
	}
	if !completeSubmittedSource(request.Source) {
		return ManagedAddResult{}, errors.New("managed source must be HTTP(S) or magnet")
	}
	current, err := state.Load(app.options.Paths.StateFile)
	if err != nil {
		return ManagedAddResult{}, err
	}
	if current.RuntimeSchemaVersion != 2 {
		return ManagedAddResult{}, errors.New("UpgradeRequired: install managed runtime v2 before adding tasks")
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
	jobID, err := randomID()
	if err != nil {
		return ManagedAddResult{}, err
	}
	gid, err := randomExecutionID(jobID)
	if err != nil {
		return ManagedAddResult{}, err
	}
	workDir := jobs.WorkDir(scope, jobID)
	if err := os.Mkdir(workDir, 0o700); err != nil {
		return ManagedAddResult{}, err
	}
	job := jobs.Job{ID: jobID, Source: request.Source, TargetDir: target.Path, TargetIdentity: jobIdentity(target.Identity), StorageID: scope.ID, ActivityIntent: jobs.ActivityRunning, Payload: jobs.PayloadState{Location: jobs.PayloadStaging}, Execution: &jobs.ExecutionBinding{GID: gid}}
	if _, err := repository.Create(job); err != nil {
		return ManagedAddResult{}, errors.Join(err, os.Remove(workDir))
	}
	result := ManagedAddResult{Task: TaskRef{JobID: jobID}}
	reconciled, err := app.ReconcileJob(ctx, jobID, ReconcileInput{Mode: ReconcileLive})
	if err != nil {
		return ManagedAddResult{}, err
	}
	result.Warning = reconciled.Warning
	if err := app.recordDir(target.Path); result.Warning == nil && err != nil {
		result.Warning = err
	}
	return result, nil
}

func (app *App) ManagedHook(ctx context.Context, _ string, gid string) error {
	if err := closerInheritedLock(); err != nil {
		return err
	}
	if !jobs.ValidID(gid) {
		return errors.New("invalid managed hook GID")
	}
	scanned, err := jobs.New(app.options.Paths.StateDir).Scan()
	if err != nil {
		return err
	}
	var matches []string
	for _, item := range scanned {
		if item.Err == nil && item.Job.Execution != nil && item.Job.Execution.GID == gid {
			matches = append(matches, item.ID)
		}
	}
	if len(matches) == 0 {
		return nil
	}
	if len(matches) != 1 {
		return errors.New("ManagedIdentityConflict: execution GID is bound by multiple manifests")
	}
	result, err := app.ReconcileJob(ctx, matches[0], ReconcileInput{Mode: ReconcileLive, ExpectedGID: gid})
	return reconcileCommandError(result, err)
}

func reconcileCommandError(result ReconcileResult, err error) error {
	if err != nil {
		return err
	}
	return result.Warning
}

func validateNative(job jobs.Job, scope jobs.StorageScope, native aria2.LifecycleStatus) error {
	if job.Execution == nil || native.GID != job.Execution.GID {
		return errors.New("lifecycle status returned a different GID")
	}
	expected := jobs.WorkDir(scope, job.ID)
	if job.Payload.Location == jobs.PayloadPublished {
		expected = job.TargetDir
	}
	if filepath.Clean(native.Dir) != filepath.Clean(expected) {
		return errors.New("native execution points outside its managed directory")
	}
	return nil
}

func executionBindingUnique(repository *jobs.Repository, job jobs.Job) (bool, error) {
	if job.Execution == nil {
		return true, nil
	}
	items, err := repository.Scan()
	if err != nil {
		return false, err
	}
	for _, item := range items {
		if item.Err == nil && item.ID != job.ID && item.Job.Execution != nil && item.Job.Execution.GID == job.Execution.GID {
			return false, nil
		}
	}
	return true, nil
}

func ensureExecutionBindingUnique(repository *jobs.Repository, job jobs.Job, token jobs.Token) error {
	unique, err := executionBindingUnique(repository, job)
	if err != nil || unique {
		return err
	}
	conflict := errors.New("ManagedIdentityConflict: execution GID is bound by multiple manifests")
	return persistIssue(repository, job, token, "ManagedIdentityConflict", conflict)
}

func validateAndDetach(ctx context.Context, env liveEnvironment, job jobs.Job, scope jobs.StorageScope) error {
	if job.Execution == nil {
		return nil
	}
	native, err := env.rpc.LifecycleStatus(ctx, env.current, job.Execution.GID)
	if aria2.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := validateNative(job, scope, native); err != nil {
		return fmt.Errorf("ManagedIdentityConflict: %w", err)
	}
	return detachManagedNative(ctx, env.rpc, env.current, job.Execution.GID)
}

func convergeActivity(ctx context.Context, env liveEnvironment, gid, status string, intent jobs.ActivityIntent) error {
	if intent == jobs.ActivityStopped && (status == "active" || status == "waiting") {
		return env.rpc.Pause(ctx, env.current, gid)
	}
	if intent == jobs.ActivityRunning && status == "paused" {
		return env.rpc.Resume(ctx, env.current, gid)
	}
	return nil
}

func stagedAddOptions(job jobs.Job, workDir string, fact StartupFact) aria2.AddOptions {
	options := aria2.AddOptions{Dir: workDir, GID: job.Execution.GID, Managed: true, Pause: job.ActivityIntent == jobs.ActivityStopped}
	if stagedIntegrityRequired(fact) {
		value := true
		options.CheckIntegrity = &value
	}
	return options
}

func transferComplete(native aria2.LifecycleStatus) bool {
	if native.InfoHash != "" {
		return native.Status == "active" && native.Seeder && native.CompletedLength == native.TotalLength
	}
	return native.Status == "complete" && native.CompletedLength == native.TotalLength
}

func checkpointAndClearIssue(ctx context.Context, repository *jobs.Repository, env liveEnvironment, job jobs.Job, token jobs.Token) (ReconcileResult, error) {
	job.Issue = nil
	next, err := repository.SaveCAS(job, token)
	if err != nil {
		return ReconcileResult{}, err
	}
	if err := env.rpc.SaveSession(ctx, env.current); err != nil {
		job.Issue = &jobs.JobIssue{Code: "RestartCheckpointFailed"}
		_, saveErr := repository.SaveCAS(job, next)
		return ReconcileResult{Warning: err}, saveErr
	}
	return ReconcileResult{}, nil
}

// managedIssueError keeps the diagnostic cause available to logs and error
// inspection while allowing user interfaces to use the issue policy's stable,
// actionable text instead of exposing reconciliation internals.
type managedIssueError struct {
	code  string
	cause error
}

func (err *managedIssueError) Error() string { return err.cause.Error() }
func (err *managedIssueError) Unwrap() error { return err.cause }

func (err *managedIssueError) UserMessage() string {
	if metadata, ok := jobs.LookupIssue(err.code); ok && metadata.Text != "" {
		return metadata.Text
	}
	return err.Error()
}

func persistIssue(repository *jobs.Repository, job jobs.Job, token jobs.Token, code string, cause error) error {
	job.Issue = &jobs.JobIssue{Code: code}
	if _, err := repository.SaveCAS(job, token); err != nil {
		return errors.Join(cause, fmt.Errorf("persist %s: %w", code, err))
	}
	return &managedIssueError{code: code, cause: cause}
}

func issueCode(err error, fallback string) string {
	if strings.Contains(err.Error(), "ManagedIdentityConflict") {
		return "ManagedIdentityConflict"
	}
	return fallback
}

func reconcilePreparedPublication(ctx context.Context, repository *jobs.Repository, job jobs.Job, token jobs.Token, scope jobs.StorageScope) (jobs.Job, jobs.Token, error) {
	unlock, err := repository.LockPublication(ctx)
	if err != nil {
		return job, token, err
	}
	defer unlock()
	return reconcilePreparedPublicationUnderLock(repository, job, token, scope)
}

func reconcilePreparedPublicationUnderLock(repository *jobs.Repository, job jobs.Job, token jobs.Token, scope jobs.StorageScope) (jobs.Job, jobs.Token, error) {
	if job.Execution != nil {
		return job, token, errors.New("PublicationRecoveryRequired: execution has not retired")
	}
	source := filepath.Join(jobs.WorkDir(scope, job.ID), job.Payload.Root)
	destination := filepath.Join(job.TargetDir, job.Payload.FinalRoot)
	sourceIdentity, sourceErr := publication.Identify(source)
	destinationIdentity, destinationErr := publication.Identify(destination)
	sourceExists, sourceUncertain := pathPresence(source, sourceErr)
	destinationExists, destinationUncertain := pathPresence(destination, destinationErr)
	if sourceExists && destinationExists {
		root, err := publication.AvailableRoot(source, job.TargetDir, job.Payload.Root)
		if err != nil {
			return job, token, persistIssue(repository, job, token, publicationProblem(err), err)
		}
		job.Payload.FinalRoot = root
		var errSave error
		token, errSave = repository.SaveCAS(job, token)
		if errSave != nil {
			return job, token, errSave
		}
		destination = filepath.Join(job.TargetDir, root)
		destinationIdentity, destinationErr = publication.Identify(destination)
		destinationExists, destinationUncertain = pathPresence(destination, destinationErr)
	}
	if sourceUncertain || destinationUncertain {
		err := errors.New("publication filesystem state is uncertain")
		return job, token, persistIssue(repository, job, token, "PublicationStateUncertain", err)
	}
	if sourceExists && !destinationExists {
		if job.Payload.Identity.ReliableAcrossRename && !sameJobIdentity(job.Payload.Identity, sourceIdentity) {
			err := errors.New("prepared source identity changed")
			return job, token, persistIssue(repository, job, token, "PublicationPayloadMismatch", err)
		}
		if !job.Payload.Identity.ReliableAcrossRename {
			job.Payload.Identity = jobIdentity(sourceIdentity)
			var err error
			token, err = repository.SaveCAS(job, token)
			if err != nil {
				return job, token, err
			}
		}
		if _, err := publication.MoveExpected(source, destination, sourceIdentity, publicationIdentity(job.TargetIdentity)); err != nil {
			return job, token, persistIssue(repository, job, token, publicationProblem(err), err)
		}
	} else if !sourceExists && destinationExists {
		if job.Payload.Identity.ReliableAcrossRename && !sameJobIdentity(job.Payload.Identity, destinationIdentity) {
			err := errors.New("prepared destination identity changed")
			return job, token, persistIssue(repository, job, token, "PublicationPayloadMismatch", err)
		}
	} else {
		err := errors.New("prepared payload is missing")
		return job, token, persistIssue(repository, job, token, "PublicationPayloadMissing", err)
	}
	job.Payload.Location = jobs.PayloadPublished
	job.Issue = nil
	if job.PublicationRenamed() {
		job.ActivityIntent = jobs.ActivityStopped
	}
	if _, err := repository.ReadMetainfo(job.ID); errors.Is(err, os.ErrNotExist) {
		job.ActivityIntent = jobs.ActivityStopped
	}
	next, err := repository.SaveCAS(job, token)
	return job, next, err
}

func sameJobIdentity(expected jobs.ObjectIdentity, actual publication.Identity) bool {
	return expected.MountID == actual.MountID && expected.ObjectID == actual.ObjectID
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

func detachManagedNative(ctx context.Context, rpc managedRPC, current state.State, gid string) error {
	native, err := rpc.LifecycleStatus(ctx, current, gid)
	if aria2.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	switch native.Status {
	case "active", "waiting", "paused":
		if err := rpc.ForceRemove(ctx, current, gid); err != nil && !aria2.IsNotFound(err) {
			return err
		}
	case "complete", "error", "removed":
	default:
		return fmt.Errorf("cannot detach managed GID from native status %q", native.Status)
	}
	return waitForManagedNativeAbsence(ctx, rpc, current, gid)
}

func waitForManagedNativeAbsence(ctx context.Context, rpc managedRPC, current state.State, gid string) error {
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	for {
		native, err := rpc.LifecycleStatus(ctx, current, gid)
		if aria2.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if native.GID != gid {
			return errors.New("ManagedIdentityConflict: detach returned a different GID")
		}
		if native.Status == "complete" || native.Status == "error" || native.Status == "removed" {
			if err := rpc.RemoveDownloadResult(ctx, current, gid); err != nil && !aria2.IsNotFound(err) {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			return errors.New("managed GID remains present after detach timeout")
		case <-time.After(25 * time.Millisecond):
		}
	}
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
		relative, err := filepath.Rel(root, filepath.Clean(file.Path))
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return false
		}
	}
	return true
}

func ensureStorageScope(repository *jobs.Repository, target publication.Target) (jobs.StorageScope, error) {
	scopes, err := repository.ScanStorages()
	if err != nil {
		return jobs.StorageScope{}, err
	}
	mountPoint, anchor := filepath.Clean(target.MountPoint), filepath.Clean(target.MountPoint)
	rootWritable := unix.Access(mountPoint, unix.W_OK) == nil
	for _, scope := range scopes {
		if filepath.Clean(scope.MountPoint) != mountPoint || (rootWritable && filepath.Clean(scope.StagingAnchor) != anchor) {
			continue
		}
		stagingRoot := filepath.Join(scope.StagingAnchor, ".aria2s_staging", scope.ID)
		if pathsOverlap(target.Path, stagingRoot) {
			return jobs.StorageScope{}, errors.New("managed target overlaps staging namespace")
		}
		marker, identifyErr := publication.Identify(stagingRoot)
		if identifyErr != nil || !publication.SameObject(marker, publicationIdentity(scope.Marker)) {
			return jobs.StorageScope{}, errors.New("StorageMismatch: registered staging marker changed")
		}
		return scope, nil
	}
	if !rootWritable {
		anchor = filepath.Dir(target.Path)
	}
	id, err := randomID()
	if err != nil {
		return jobs.StorageScope{}, err
	}
	root := filepath.Join(anchor, ".aria2s_staging", id)
	if pathsOverlap(target.Path, root) {
		return jobs.StorageScope{}, errors.New("managed target overlaps staging namespace")
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

func randomExecutionID(jobID string) (string, error) {
	for {
		gid, err := randomID()
		if err != nil || gid != jobID {
			return gid, err
		}
	}
}
func jobIdentity(identity publication.Identity) jobs.ObjectIdentity {
	return jobs.ObjectIdentity{MountID: identity.MountID, ObjectID: identity.ObjectID, ReliableAcrossRename: identity.ReliableAcrossRename}
}
func publicationIdentity(identity jobs.ObjectIdentity) publication.Identity {
	return publication.Identity{MountID: identity.MountID, ObjectID: identity.ObjectID, ReliableAcrossRename: identity.ReliableAcrossRename}
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
	data, err := os.ReadFile(filepath.Join(workDir, strings.ToLower(infoHash)+".torrent"))
	if err != nil {
		return nil, err
	}
	actual, err := aria2.ValidateMetainfo(data)
	if err != nil || !strings.EqualFold(actual, infoHash) {
		return nil, errors.New("resolved torrent metainfo failed validation")
	}
	return data, nil
}

func readValidatedMetainfo(repository *jobs.Repository, jobID string) ([]byte, error) {
	metainfo, err := repository.ReadMetainfo(jobID)
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
		// aria2 exposes a magnet metadata task while --bt-save-metadata is
		// still producing the hash-named torrent. Active, waiting, and paused
		// statuses therefore do not prove that the descriptor is durable yet.
		if errors.Is(err, os.ErrNotExist) {
			switch native.Status {
			case "active", "waiting", "paused":
				return nil, false, nil
			}
		}
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
	if name == root+".aria2" || strings.HasSuffix(name, ".torrent") || name == ".DS_Store" || name == "._.DS_Store" {
		return true
	}
	companion, found := strings.CutPrefix(name, "._")
	return found && (companion == root+".aria2" || strings.HasSuffix(companion, ".torrent"))
}

func removeWorkDir(workDir string) error {
	if err := os.RemoveAll(workDir); err != nil {
		return err
	}
	return atomicfile.SyncDirectory(filepath.Dir(workDir))
}
