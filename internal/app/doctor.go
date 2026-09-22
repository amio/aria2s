package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/doctor"
	"github.com/amio/aria2s/internal/jobs"
	"github.com/amio/aria2s/internal/publication"
	"github.com/amio/aria2s/internal/state"
)

// Doctor observes durable and native facts without preparing a Dashboard,
// reconciling jobs, taking write locks, rebinding storage, or starting services.
func (app *App) Doctor(ctx context.Context) doctor.Report {
	return doctor.Check(ctx, doctor.Options{
		Paths: app.options.Paths, IsPortAvailable: app.options.IsPortAvailable,
		Service: app.options.Service, RPCProbeTimeout: app.options.RPCProbeTimeout,
		RPCSlowThreshold:    app.options.RPCSlowThreshold,
		RPCVersion:          app.options.RPC.Version,
		InspectInstallation: app.inspectDoctorInstallation,
		InspectTasks:        app.inspectDoctorTasks,
	})
}

func (app *App) inspectDoctorInstallation(current state.State) error {
	if err := app.inspectInstalledRuntime(current); err != nil {
		return err
	}
	info, err := os.Stat(app.options.Paths.ConfigFile)
	if err != nil {
		return fmt.Errorf("read aria2 configuration: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("aria2 configuration is not a regular file")
	}
	file, err := os.Open(app.options.Paths.ConfigFile)
	if err != nil {
		return fmt.Errorf("read aria2 configuration: %w", err)
	}
	return file.Close()
}

type doctorRPC interface {
	CompleteCensus(context.Context, state.State) ([]aria2.LifecycleStatus, error)
	LifecycleStatus(context.Context, state.State, string) (aria2.LifecycleStatus, error)
}

func taskCheck(name, severity, code, summary string) doctor.DiagnosticCheck {
	return doctor.DiagnosticCheck{Group: doctor.Downloads, Name: name,
		Issue: doctor.Issue{Code: code, Severity: severity, Summary: summary}}
}

func (app *App) inspectDoctorTasks(ctx context.Context, current state.State, reachable bool, scanned []jobs.ScannedJob, scanErr error) []doctor.DiagnosticCheck {
	var checks []doctor.DiagnosticCheck
	ownershipComplete := scanErr == nil
	if scanErr != nil {
		check := taskCheck("Task records", doctor.Error, "TaskStoreUnreadable", "managed task records could not be read")
		check.Evidence = scanErr.Error()
		check.Recovery = []string{"Restore access to the aria2s state directory, then rerun `aria2s doctor`."}
		checks = append(checks, check)
	} else {
		corrupt := 0
		for _, item := range scanned {
			if item.Err != nil {
				corrupt++
				ownershipComplete = false
			}
		}
		severity := doctor.OK
		if corrupt > 0 {
			severity = doctor.Error
		}
		checks = append(checks, taskCheck("Task records", severity, "", fmt.Sprintf("%d managed tasks; %d unreadable records", len(scanned), corrupt)))
	}
	rpc, supported := app.options.RPC.(doctorRPC)
	liveCtx, cancel := context.WithTimeout(ctx, app.options.DashboardReadTimeout)
	defer cancel()
	live := reachable && supported && current.RuntimeSchemaVersion == 2
	var native []aria2.LifecycleStatus
	var censusErr error
	if live {
		native, censusErr = rpc.CompleteCensus(liveCtx, current)
		live = censusErr == nil
	}
	if !live {
		native = nil
		check := taskCheck("Live task coverage", doctor.Skipped, "TaskObservationUnavailable", "live task status could not be checked; local records are still inspected")
		if censusErr != nil {
			check.Evidence = censusErr.Error()
		}
		check.Recovery = []string{"Resolve the service/RPC findings, then rerun `aria2s doctor` to verify live downloads."}
		checks = append(checks, check)
	} else {
		checks = append(checks, taskCheck("Live task coverage", doctor.OK, "", "active, waiting, and retained stopped tasks were read"))
	}
	byGID := make(map[string]aria2.LifecycleStatus)
	for _, task := range native {
		byGID[task.GID] = task
	}
	bindings := make(map[string]int)
	for _, item := range scanned {
		if item.Err == nil && item.Job.Execution != nil {
			bindings[item.Job.Execution.GID]++
		}
	}
	repository := jobs.New(app.options.Paths.StateDir)
	states := make(map[string]int)
	observationFailures := 0
	var observationEvidence string
	for _, item := range scanned {
		if ctx.Err() != nil {
			checks = append(checks, taskCheck("Remaining tasks", doctor.Skipped, "TaskInspectionCanceled", "task inspection was canceled before all local records were checked"))
			break
		}
		if item.Err != nil {
			check := taskCheck("Unreadable task", doctor.Error, "CorruptManifest", "managed task metadata is corrupt")
			check.Issue = doctor.LifecycleProblem("CorruptManifest", item.ID, item.Err.Error())
			check.TaskID, check.TaskStatus, check.Ownership = item.ID, "error", "managed"
			checks = append(checks, check)
			states["error"]++
			continue
		}
		job := item.Job
		nativeTask, exists := aria2.LifecycleStatus{}, false
		known := live
		if job.Execution != nil {
			nativeTask, exists = byGID[job.Execution.GID]
			// Pagination is not atomic: confirm an omitted managed execution
			// directly before turning that omission into an absence diagnosis.
			if live && !exists {
				var err error
				nativeTask, err = rpc.LifecycleStatus(liveCtx, current, job.Execution.GID)
				exists = err == nil && nativeTask.GID != ""
				known = exists || aria2.IsNotFound(err)
				if !known {
					observationFailures++
					if err != nil {
						observationEvidence = err.Error()
					}
				}
			}
		}
		check := app.inspectDoctorJob(repository, job, nativeTask, exists, known, job.Execution != nil && bindings[job.Execution.GID] > 1)
		checks = append(checks, check)
		states[check.TaskStatus]++
	}
	if observationFailures > 0 {
		check := taskCheck("Task observations", doctor.Skipped, "TaskObservationUnavailable", fmt.Sprintf("could not confirm %d managed executions; their live status remains unknown", observationFailures))
		check.Evidence = observationEvidence
		check.Recovery = []string{"Rerun `aria2s doctor` when RPC is responsive to verify the remaining task executions."}
		checks = append(checks, check)
	}
	for _, task := range byGID {
		if bindings[task.GID] > 0 {
			continue
		}
		// Completed metadata is an intermediate aria2 artifact, not a second download.
		if task.Status == "complete" && isMetadataStatus(task) {
			continue
		}
		projection := ProjectTask(TaskFacts{NativeStatus: task.Status, NativeSeeder: task.Seeder, NativeMetadata: isMetadataStatus(task)})
		check := taskCheck(firstNonempty(task.Name, "Native task"), doctor.OK, "", string(projection.Status)+" (unmanaged)")
		check.TaskID, check.TaskStatus, check.Ownership = task.GID, string(projection.Status), string(projection.Ownership)
		if !ownershipComplete {
			check.Ownership = "unknown"
			check.Summary = string(projection.Status) + " (ownership unverified)"
		}
		if projection.Status == StatusError {
			check.Severity, check.Code = doctor.Error, "NativeTaskError"
			check.Summary = "aria2 reports a failed or removed transfer"
			check.Evidence = nativeTaskEvidence(task)
			check.Recovery = []string{"Open `aria2s dashboard`, inspect the failed transfer's details, and use its available actions."}
		}
		checks = append(checks, check)
		states[check.TaskStatus]++
	}
	var parts []string
	for _, status := range []string{"downloading", "metadata", "seeding", "waiting", "paused", "complete", "error", "unknown"} {
		if count := states[status]; count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", count, status))
		}
	}
	if len(parts) > 0 {
		summary := taskCheck("Task states", doctor.OK, "", strings.Join(parts, ", "))
		summary.Explanation = "A point-in-time observation; waiting, pausing, seeding, and zero speed alone are not failures."
		checks = append(checks, summary)
	} else if live && scanErr == nil && len(scanned) == 0 && len(byGID) == 0 {
		checks = append(checks, taskCheck("Task states", doctor.OK, "", "no existing download tasks"))
	}
	// A deterministic report is easier to compare across runs and output modes.
	sort.SliceStable(checks, func(i, j int) bool {
		if (checks[i].TaskID == "") != (checks[j].TaskID == "") {
			return checks[i].TaskID == ""
		}
		return checks[i].TaskID < checks[j].TaskID
	})
	return checks
}

func (app *App) inspectDoctorJob(repository *jobs.Repository, job jobs.Job, native aria2.LifecycleStatus, exists, known, duplicate bool) doctor.DiagnosticCheck {
	code, evidence := issueCodeForJob(job), ""
	var scope jobs.StorageScope
	var storageErr error
	if job.Removed {
		// Removal owns only staging cleanup. A missing target must not block it.
		scope, storageErr = loadRegisteredStorageScope(repository, job.StorageID)
		if storageErr == nil {
			scope, _, storageErr = observeStorageScope(scope)
		}
		code = "RemovalFailed"
	} else {
		_, scope, job, _, storageErr = observeJobStorage(repository, job)
		if storageErr != nil {
			code = storageFailureIssueCode(storageErr)
		}
	}
	if storageErr != nil {
		evidence = storageErr.Error()
	}
	if storageErr == nil && !job.Removed && job.Payload.Location == jobs.PayloadPublished {
		payloadPath, identity, err := publication.ValidatePayloadRoot(job.TargetDir, job.FinalRoot())
		if err != nil || identity.MountID != job.Payload.Identity.MountID || (job.Payload.Identity.ReliableAcrossRename && identity.ObjectID != job.Payload.Identity.ObjectID) {
			code = "FinalSeedPathMismatch"
			evidence = payloadPath + ": published payload is missing or its root identity changed"
			if err != nil {
				evidence += ": " + err.Error()
			}
		}
	}
	conflict := duplicate
	if exists && storageErr == nil && job.Execution != nil && !job.Removed {
		conflict = conflict || validateNative(job, scope, native) != nil
		if job.Payload.Location == jobs.PayloadPublished {
			conflict = conflict || !publishedFilesMatch(native.Files, filepath.Join(job.TargetDir, job.FinalRoot()))
		}
	}
	if conflict && !job.Removed {
		code, evidence = "ManagedIdentityConflict", "native GID or download path does not uniquely match its managed record"
	}
	facts := TaskFacts{Managed: true, Lifecycle: derivedLifecycle(job), Intent: job.ActivityIntent, IssueCode: code,
		NativeStatus: native.Status, NativeSeeder: native.Seeder, NativeMetadata: isMetadataStatus(native),
		NativeAbsent: known && !exists, IdentityConflict: conflict}
	projection := ProjectTask(facts)
	check := taskCheck(firstNonempty(job.FinalRoot(), job.DisplayName, native.Name, "Managed task"), doctor.OK, "", string(projection.Status))
	check.TaskID, check.TaskStatus, check.Ownership = job.ID, string(projection.Status), "managed"
	if !known && code == "" {
		check.TaskStatus, check.Summary = "unknown", "local checks passed; live status not checked"
		return check
	}
	if projection.IssueCode != "" {
		if code == "" {
			evidence = "prepared payload is detached; publication has not been committed"
		}
		check.Issue = doctor.LifecycleProblem(projection.IssueCode, job.ID, firstNonempty(evidence, "persisted task issue"))
	}
	if !known && check.Severity != doctor.Error {
		check.TaskStatus = "unknown"
	}
	if known && projection.Status == StatusError && check.Severity != doctor.Error {
		check.Severity, check.Code = doctor.Error, "TaskExecutionMissing"
		check.Summary = "task expects an execution that aria2 no longer retains"
		if exists {
			check.Code, check.Summary = "NativeTaskError", "aria2 reports a failed or removed transfer"
			check.Evidence = nativeTaskEvidence(native)
		}
		check.Recovery = []string{"Open `aria2s dashboard`, inspect this task, and use Retry or Remove after correcting the reported condition."}
	}
	if check.Severity == doctor.OK && exists && job.Payload.Location == jobs.PayloadStaging && native.Status == "complete" && !isMetadataStatus(native) {
		check.Severity, check.Code = doctor.Warning, "PublicationPending"
		check.Summary = "transfer finished; payload publication has not completed"
		check.Explanation = "The completion hook may still be running. This observation alone does not prove a stalled publication."
		check.Recovery = []string{"Rerun `aria2s doctor` after completion settles; if publication remains pending, inspect `aria2s logs`."}
	}
	if check.Severity != doctor.OK {
		// The projection is authoritative for available actions, particularly
		// removal-only jobs and corrupt/ambiguous ownership.
		if job.Removed {
			check.Recovery = []string{"Restore staging storage access, then choose Remove in Dashboard to finish removal; published files are retained."}
		}
		if check.Explanation == check.Summary {
			check.Explanation = ""
		}
		check.Explanation = strings.TrimSpace(check.Explanation + " Task " + job.ID + "; status: " + check.TaskStatus)
		if len(projection.Actions) > 0 {
			check.Explanation += "; Dashboard actions: " + strings.Join(projection.Actions, ", ")
		}
	}
	return check
}

func nativeTaskEvidence(task aria2.LifecycleStatus) string {
	return fmt.Sprintf("native status=%s; error=%s; %s", task.Status, task.ErrorCode, task.ErrorMessage)
}
