package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/jobs"
	"github.com/amio/aria2s/internal/publication"
	managedruntime "github.com/amio/aria2s/internal/runtime"
	"github.com/amio/aria2s/internal/state"
)

func (app *App) ManagedExec(ctx context.Context) error {
	current, err := state.Load(app.options.Paths.StateFile)
	if err != nil {
		return err
	}
	if current.RuntimeSchemaVersion != 2 || current.SessionPath != app.options.Paths.SessionFile || current.StartupInputPath != app.options.Paths.StartupInputFile {
		return errors.New("UpgradeRequired: managed-exec requires committed runtime schema v2")
	}
	controller, err := os.Executable()
	if err != nil {
		return err
	}
	controller, err = filepath.EvalSymlinks(controller)
	if err != nil {
		return err
	}
	identity, err := fileIdentity(controller)
	if err != nil {
		return err
	}
	if controller != current.ControllerPath || identity != current.ControllerIdentity {
		return errors.New("InstallIncomplete: controller executable identity does not match committed v2 state")
	}
	serviceInfo, err := os.Lstat(app.options.Paths.ServiceFile)
	if err != nil || !serviceInfo.Mode().IsRegular() || serviceInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("InstallIncomplete: service artifact is not a regular file")
	}
	serviceData, err := os.ReadFile(app.options.Paths.ServiceFile)
	if err != nil {
		return err
	}
	serviceHash := fmt.Sprintf("%x", sha256.Sum256(serviceData))
	if serviceHash != current.ServiceIdentity {
		return errors.New("InstallIncomplete: service artifact identity does not match committed v2 state")
	}
	lease, err := managedruntime.Acquire(app.options.Paths.InstanceLockFile)
	if err != nil {
		return err
	}
	defer lease.Close()
	progressPath := app.options.Paths.StartupProgressFile
	cleanupProgress := true
	defer func() {
		if cleanupProgress {
			_ = clearStartupProgress(progressPath)
		}
	}()
	_ = persistStartupProgress(progressPath, startupProgress{phase: startupPhaseStarting})
	repository := jobs.New(app.options.Paths.StateDir)
	scanned, err := repository.Scan()
	if err != nil {
		return err
	}
	nativeData, err := os.ReadFile(current.SessionPath)
	if errors.Is(err, os.ErrNotExist) {
		nativeData = nil
	} else if err != nil {
		return err
	}
	blocks, parseProblems := aria2.ParseSession(nativeData)
	for _, problem := range parseProblems {
		// Invalid blocks are omitted by the parser. Reconciliation then either uses
		// an explicitly safe fallback or persists RestartStateMissing for any
		// managed job with artifacts; stderr keeps unscoped corruption visible.
		fmt.Fprintf(os.Stderr, "aria2s: ignored corrupt native session block: %v\n", problem)
	}
	byGID := make(map[string][]aria2.SessionBlock)
	for _, block := range blocks {
		if gid, ok := block.Option("gid"); ok {
			byGID[gid] = append(byGID[gid], block)
		}
	}
	boundCounts := make(map[string]int)
	for _, item := range scanned {
		if item.Err == nil && item.Job.Execution != nil {
			boundCounts[item.Job.Execution.GID]++
		}
	}
	var owned []aria2.SessionBlock
	for index, item := range scanned {
		_ = persistStartupProgress(progressPath, startupProgress{phase: startupPhaseChecking, current: index + 1, total: len(scanned)})
		if item.Err != nil {
			fmt.Fprintf(os.Stderr, "aria2s: ignored corrupt managed manifest %s: %v\n", item.ID, item.Err)
			continue
		}
		var saved *aria2.SessionBlock
		duplicate := false
		if item.Job.Execution != nil {
			matches := byGID[item.Job.Execution.GID]
			duplicate = len(matches) > 1 || boundCounts[item.Job.Execution.GID] > 1
			if len(matches) == 1 {
				block := matches[0]
				saved = &block
			}
		}
		result, reconcileErr := app.ReconcileJob(ctx, item.ID, ReconcileInput{Mode: ReconcileStartup, SavedBlock: saved, SavedDuplicate: duplicate})
		if reconcileErr != nil {
			fmt.Fprintf(os.Stderr, "aria2s: omitted managed job %s: %v\n", item.ID, reconcileErr)
			continue
		}
		if result.StartupBlock != nil {
			owned = append(owned, *result.StartupBlock)
		}
	}
	encoded, err := aria2.EncodeSession(owned)
	if err != nil {
		return err
	}
	if err := managedruntime.WriteStartup(current.StartupInputPath, encoded); err != nil {
		return err
	}
	for _, event := range []string{"on-download-complete", "on-bt-download-complete"} {
		if err := managedruntime.WriteHook(filepath.Join(app.options.Paths.HooksDir, event), current.ControllerPath, event); err != nil {
			return err
		}
	}
	safeStartup, err := managedruntime.SafeStartupEnabled(app.options.Paths.SafeStartupFile)
	if err != nil {
		return err
	}
	args := managedRuntimeArgs(current, app.options.Paths.HooksDir, safeStartup)
	environment := append(os.Environ(), lease.Environment())
	_ = persistStartupProgress(progressPath, startupProgress{phase: startupPhaseWaitingRPC})
	if err := managedExec(current.Aria2cPath, args, environment); err != nil {
		return err
	}
	cleanupProgress = false
	return nil
}

func managedRuntimeArgs(current state.State, hooksDir string, safeStartup bool) []string {
	args := aria2.ManagedV2Args(current, hooksDir)
	if safeStartup {
		args = append(args, "--file-allocation=none")
	}
	return args
}

var managedExec = managedruntime.Exec

func storageMatches(scope jobs.StorageScope, job jobs.Job) bool {
	return storageIdentityMatches(scope, job)
}

func storageIdentityMatches(scope jobs.StorageScope, job jobs.Job) bool {
	marker, err := publication.Identify(filepath.Join(scope.StagingAnchor, ".aria2s_staging", scope.ID))
	if err != nil || marker.MountID != scope.Marker.MountID || marker.ObjectID != scope.Marker.ObjectID {
		return false
	}
	target, err := publication.Identify(job.TargetDir)
	return err == nil && target.MountID == job.TargetIdentity.MountID && target.ObjectID == job.TargetIdentity.ObjectID
}

func pathPresence(path string, identifyErr error) (exists, uncertain bool) {
	if identifyErr == nil {
		return true, false
	}
	_, err := os.Lstat(path)
	if err == nil {
		return true, true
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, false
	}
	return false, true
}

func inspectStartupFact(repository *jobs.Repository, job jobs.Job, scope jobs.StorageScope, available bool) StartupFact {
	fact := StartupFact{StorageAvailable: available}
	if !available {
		return fact
	}
	if metainfo, err := repository.ReadMetainfo(job.ID); err == nil {
		if _, validationErr := aria2.ValidateMetainfo(metainfo); validationErr != nil {
			return fact
		}
		fact.HasMetainfo = true
		fact.MetainfoPath = repository.MetainfoPath(job.ID)
		fact.Torrent = true
	}
	// Published jobs are reconstructed from retained metainfo at the final
	// target. Their staging directory is no longer part of startup truth.
	if job.Payload.Location == jobs.PayloadPublished {
		return fact
	}
	workDir := jobs.WorkDir(scope, job.ID)
	entries, err := os.ReadDir(workDir)
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(entries) == 0) {
		fact.WorkEmpty = true
		return fact
	}
	if err != nil {
		return fact
	}
	roots := make(map[string]struct{})
	controls := make([]string, 0)
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".aria2") {
			controls = append(controls, strings.TrimSuffix(name, ".aria2"))
			continue
		}
		if strings.HasSuffix(name, ".torrent") && fact.HasMetainfo {
			candidate, readErr := os.ReadFile(filepath.Join(workDir, name))
			retained, retainedErr := repository.ReadMetainfo(job.ID)
			candidateHash, candidateErr := aria2.ValidateMetainfo(candidate)
			retainedHash, retainedValidationErr := aria2.ValidateMetainfo(retained)
			if readErr == nil && retainedErr == nil && candidateErr == nil && retainedValidationErr == nil && candidateHash == retainedHash {
				continue
			}
		}
		roots[name] = struct{}{}
	}
	for _, base := range controls {
		if _, ok := roots[base]; !ok && base != job.Payload.Root {
			return fact
		}
	}
	fact.HasControl = len(controls) > 0
	if job.Payload.Root != "" {
		if len(roots) == 1 {
			if _, ok := roots[job.Payload.Root]; ok {
				fact.InferredRoot = job.Payload.Root
			}
		}
		return fact
	}
	if len(roots) == 1 {
		for root := range roots {
			fact.InferredRoot = root
		}
	}
	return fact
}
