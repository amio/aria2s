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
	repository := jobs.New(app.options.Paths.StateDir)
	scanned, err := repository.Scan()
	if err != nil {
		return err
	}
	scopes, err := repository.ScanStorages()
	if err != nil {
		return err
	}
	storageMap := make(map[string]jobs.StorageScope, len(scopes))
	for _, scope := range scopes {
		storageMap[scope.ID] = scope
	}
	manifests := make([]jobs.Job, 0, len(scanned))
	facts := make(map[string]StartupFact, len(scanned))
	for _, scannedJob := range scanned {
		if scannedJob.Err != nil {
			fmt.Fprintf(os.Stderr, "aria2s: ignored corrupt managed manifest %s: %v\n", scannedJob.ID, scannedJob.Err)
			continue
		}
		job := scannedJob.Job
		scope, ok := storageMap[job.StorageID]
		if !ok {
			manifests = append(manifests, job)
			continue
		}
		available := storageMatches(scope, job)
		if available && job.Phase == jobs.PhasePublishing {
			job, scannedJob.Token, err = reconcilePublishing(repository, job, scannedJob.Token, scope)
			if err != nil {
				return err
			}
		}
		fact := inspectStartupFact(repository, job, scope, available)
		if available && job.PayloadRoot == "" && fact.InferredRoot != "" {
			job.PayloadRoot = fact.InferredRoot
			next, saveErr := repository.SaveCAS(job, scannedJob.Token)
			if saveErr != nil {
				return saveErr
			}
			scannedJob.Token = next
		}
		facts[job.ID] = fact
		manifests = append(manifests, job)
	}
	nativeData, err := os.ReadFile(current.SessionPath)
	if errors.Is(err, os.ErrNotExist) {
		nativeData = nil
	} else if err != nil {
		return err
	}
	blocks, parseProblems := aria2.ParseSession(nativeData)
	for _, problem := range parseProblems {
		// Invalid blocks are omitted by the parser. The planner then either uses
		// an explicitly safe fallback or persists RestartStateMissing for any
		// managed job with artifacts; stderr keeps unscoped corruption visible.
		fmt.Fprintf(os.Stderr, "aria2s: ignored corrupt native session block: %v\n", problem)
	}
	plan := PlanStartup(manifests, storageMap, facts, blocks)
	problemByJob := make(map[string]string, len(plan.Problems))
	for _, problem := range plan.Problems {
		problemByJob[problem.JobID] = problem.Code
		job, token, loadErr := repository.Load(problem.JobID)
		if loadErr != nil {
			return loadErr
		}
		if job.ProblemCode != problem.Code {
			job.ProblemCode = problem.Code
			if _, saveErr := repository.SaveCAS(job, token); saveErr != nil {
				return saveErr
			}
		}
	}
	for _, job := range manifests {
		if _, failed := problemByJob[job.ID]; failed || !transientStartupProblem(job.ProblemCode) {
			continue
		}
		currentJob, token, loadErr := repository.Load(job.ID)
		if loadErr != nil {
			return loadErr
		}
		if transientStartupProblem(currentJob.ProblemCode) {
			currentJob.ProblemCode = ""
			if _, saveErr := repository.SaveCAS(currentJob, token); saveErr != nil {
				return saveErr
			}
		}
	}
	encoded, err := aria2.EncodeSession(plan.Blocks)
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
	args := aria2.ManagedV2Args(current, app.options.Paths.HooksDir)
	environment := append(os.Environ(), lease.Environment())
	return managedExec(current.Aria2cPath, args, environment)
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

func reconcilePublishing(repository *jobs.Repository, job jobs.Job, token jobs.Token, scope jobs.StorageScope) (jobs.Job, jobs.Token, error) {
	source := filepath.Join(jobs.WorkDir(scope, job.ID), job.PayloadRoot)
	destination := filepath.Join(job.TargetDir, job.PayloadRoot)
	sourceIdentity, sourceErr := publication.Identify(source)
	destinationIdentity, destinationErr := publication.Identify(destination)
	sourceExists, sourceUncertain := pathPresence(source, sourceErr)
	destinationExists, destinationUncertain := pathPresence(destination, destinationErr)
	if sourceExists && destinationExists {
		job.ProblemCode = "PublicationConflict"
		next, saveErr := repository.SaveCAS(job, token)
		return job, next, saveErr
	}
	if sourceUncertain || destinationUncertain {
		job.ProblemCode = "PublicationStateUncertain"
		next, saveErr := repository.SaveCAS(job, token)
		return job, next, saveErr
	}
	if sourceExists && !destinationExists {
		if job.PayloadIdentity.ReliableAcrossRename && (sourceIdentity.MountID != job.PayloadIdentity.MountID || sourceIdentity.ObjectID != job.PayloadIdentity.ObjectID) {
			job.ProblemCode = "PublicationPayloadMismatch"
			next, saveErr := repository.SaveCAS(job, token)
			return job, next, saveErr
		}
		if !job.PayloadIdentity.ReliableAcrossRename {
			job.PayloadIdentity = jobIdentity(sourceIdentity)
			next, saveErr := repository.SaveCAS(job, token)
			if saveErr != nil {
				return job, token, saveErr
			}
			token = next
		}
		if move, err := publication.MoveExpected(source, destination, sourceIdentity, publicationIdentity(job.TargetIdentity)); err == nil {
			finalizeReconciledPublication(repository, &job)
			if move.DirectorySyncUnsupported && job.ProblemCode == "" {
				job.ProblemCode = "PowerLossDurabilityUnavailable"
			}
			next, saveErr := repository.SaveCAS(job, token)
			return job, next, saveErr
		} else {
			job.ProblemCode = publicationProblem(err)
		}
	} else if !sourceExists && destinationExists {
		if !job.PayloadIdentity.ReliableAcrossRename || (destinationIdentity.MountID == job.PayloadIdentity.MountID && destinationIdentity.ObjectID == job.PayloadIdentity.ObjectID) {
			finalizeReconciledPublication(repository, &job)
			next, saveErr := repository.SaveCAS(job, token)
			return job, next, saveErr
		} else {
			job.ProblemCode = "PublicationPayloadMismatch"
		}
	} else {
		job.ProblemCode = "PublicationPayloadMissing"
	}
	next, saveErr := repository.SaveCAS(job, token)
	return job, next, saveErr
}

func finalizeReconciledPublication(repository *jobs.Repository, job *jobs.Job) {
	job.Phase = jobs.PhasePublished
	job.ProblemCode = ""
	if _, err := readValidatedMetainfo(repository, job.ID); err == nil {
		return
	} else if errors.Is(err, os.ErrNotExist) {
		// Plain HTTP has no retained metainfo and cannot become a final seed. A
		// crash before the hook commit must converge its intent with the phase.
		job.ActivityIntent = jobs.ActivityStopped
	} else {
		// A descriptor downloaded over HTTP can promote into BitTorrent. Preserve
		// its seed intent when retained metainfo exists but is no longer valid,
		// and surface the corruption rather than silently treating it as HTTP.
		job.ProblemCode = "RestartStateMissing"
	}
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
		if _, ok := roots[base]; !ok && base != job.PayloadRoot {
			return fact
		}
	}
	if job.PayloadRoot != "" {
		if len(roots) == 1 {
			if _, ok := roots[job.PayloadRoot]; ok {
				fact.InferredRoot = job.PayloadRoot
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

func transientStartupProblem(code string) bool {
	switch code {
	case "StorageOffline", "RestartStateMissing":
		return true
	default:
		return false
	}
}

func managedExecProblem(plan StartupPlan) error {
	if len(plan.Problems) == 0 {
		return nil
	}
	return fmt.Errorf("managed startup omitted %d job(s)", len(plan.Problems))
}
