package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/doctor"
	"github.com/amio/aria2s/internal/jobs"
	"github.com/amio/aria2s/internal/publication"
	"github.com/amio/aria2s/internal/state"
)

type doctorTestRPC struct {
	*reconcilerRPC
	censusErr   error
	omit        bool
	statusErr   error
	wait        bool
	censusCalls int
	statusCalls int
}

func (rpc *doctorTestRPC) CompleteCensus(ctx context.Context, _ state.State) ([]aria2.LifecycleStatus, error) {
	rpc.censusCalls++
	if rpc.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if rpc.censusErr != nil {
		return nil, rpc.censusErr
	}
	if rpc.omit {
		return nil, nil
	}
	var result []aria2.LifecycleStatus
	for _, task := range rpc.statuses {
		result = append(result, task)
	}
	return result, nil
}
func (rpc *doctorTestRPC) LifecycleStatus(ctx context.Context, current state.State, gid string) (aria2.LifecycleStatus, error) {
	rpc.statusCalls++
	if rpc.statusErr != nil {
		return aria2.LifecycleStatus{}, rpc.statusErr
	}
	return rpc.reconcilerRPC.LifecycleStatus(ctx, current, gid)
}

func doctorFixture(t *testing.T) (*App, *jobs.Repository, *doctorTestRPC, jobs.Job) {
	t.Helper()
	application, repository, native, target := newReconcilerTestApp(t)
	added, err := application.AddManaged(t.Context(), AddRequest{Source: "https://example.test/private?token=secret", TargetDir: target})
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := repository.Load(added.Task.JobID)
	if err != nil {
		t.Fatal(err)
	}
	rpc := &doctorTestRPC{reconcilerRPC: native}
	application.options.RPC = rpc
	return application, repository, rpc, job
}
func inspectTasks(t *testing.T, application *App, repository *jobs.Repository, reachable bool) doctor.Report {
	t.Helper()
	scanned, err := repository.Scan()
	return doctor.Report{Checks: application.inspectDoctorTasks(t.Context(), state.State{RuntimeSchemaVersion: 2}, reachable, scanned, err)}
}
func taskResult(t *testing.T, report doctor.Report, id string) doctor.DiagnosticCheck {
	t.Helper()
	for _, check := range report.Checks {
		if check.TaskID == id {
			return check
		}
	}
	t.Fatalf("no task %s: %+v", id, report.Checks)
	return doctor.DiagnosticCheck{}
}

func TestDoctorTaskStatesUseProductProjection(t *testing.T) {
	for _, test := range []struct {
		name, native, want string
		seeder, metadata   bool
	}{
		{"download", "active", "downloading", false, false},
		{"seed", "active", "seeding", true, false},
		{"metadata", "active", "metadata", false, true},
		{"waiting", "waiting", "waiting", false, false},
		{"paused", "paused", "paused", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			application, repository, rpc, job := doctorFixture(t)
			native := rpc.statuses[job.Execution.GID]
			native.Status, native.Seeder = test.native, test.seeder
			if test.metadata {
				native.InfoHash = "hash"
				native.Files = []aria2.DownloadFile{{Path: "[METADATA]file"}}
			}
			rpc.statuses[job.Execution.GID] = native
			report := inspectTasks(t, application, repository, true)
			check := taskResult(t, report, job.ID)
			if check.TaskStatus != test.want || check.Severity != doctor.OK || !report.Healthy() || !report.Complete() {
				t.Fatalf("unexpected report: %+v", report)
			}
		})
	}
}

func TestDoctorKeepsLocalEvidenceWhenLiveCoverageFails(t *testing.T) {
	for _, mode := range []string{"offline", "timeout", "fault"} {
		t.Run(mode, func(t *testing.T) {
			application, repository, rpc, job := doctorFixture(t)
			rpc.wait = mode == "timeout"
			if mode == "fault" {
				rpc.censusErr = errors.New("nested RPC fault")
			}
			application.options.DashboardReadTimeout = time.Millisecond
			report := inspectTasks(t, application, repository, mode != "offline")
			check := taskResult(t, report, job.ID)
			if check.TaskStatus != "unknown" || check.Severity != doctor.OK || report.Complete() {
				t.Fatalf("unknown became a failure/absence: %+v", report)
			}
			if mode == "offline" && rpc.censusCalls != 0 {
				t.Fatal("queried unavailable RPC")
			}
			if err := os.Rename(job.TargetDir, job.TargetDir+"-moved"); err != nil {
				t.Fatal(err)
			}
			report = inspectTasks(t, application, repository, false)
			check = taskResult(t, report, job.ID)
			if check.Code != "TargetUnavailable" {
				t.Fatalf("lost local finding: %+v", check)
			}
		})
	}
}

func TestDoctorConfirmsCensusOmissionsAndPreservesUncertainty(t *testing.T) {
	application, repository, rpc, job := doctorFixture(t)
	rpc.omit = true
	check := taskResult(t, inspectTasks(t, application, repository, true), job.ID)
	if check.TaskStatus != "downloading" || rpc.statusCalls != 1 {
		t.Fatalf("census omission was treated as absence: %+v", check)
	}
	rpc.statusErr = context.DeadlineExceeded
	report := inspectTasks(t, application, repository, true)
	if taskResult(t, report, job.ID).TaskStatus != "unknown" || report.Complete() {
		t.Fatalf("failed observation became absence: %+v", report)
	}
	rpc.statusErr = &aria2.RPCError{Code: 1, Message: "not found"}
	check = taskResult(t, inspectTasks(t, application, repository, true), job.ID)
	if check.Code != "TaskExecutionMissing" {
		t.Fatalf("confirmed absence missing: %+v", check)
	}
}

func TestDoctorStorageObservationDoesNotPersistRebinding(t *testing.T) {
	application, repository, _, job := doctorFixture(t)
	job, token, err := repository.Load(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := makeStoredMountIDsStale(repository, job, token); err != nil {
		t.Fatal(err)
	}
	beforeJob, beforeToken, _ := repository.Load(job.ID)
	beforeScope, _ := repository.LoadStorage(job.StorageID)
	check := taskResult(t, inspectTasks(t, application, repository, true), job.ID)
	if check.Severity != doctor.OK {
		t.Fatalf("remount falsely rejected: %+v", check)
	}
	afterJob, afterToken, _ := repository.Load(job.ID)
	afterScope, _ := repository.LoadStorage(job.StorageID)
	if !reflect.DeepEqual(beforeJob, afterJob) || beforeToken != afterToken || beforeScope != afterScope {
		t.Fatal("Doctor persisted storage rebinding")
	}
}

func TestDoctorRemovalSkipsMissingTargetAndNeverSuggestsRetry(t *testing.T) {
	application, repository, _, job := doctorFixture(t)
	job, token, _ := repository.Load(job.ID)
	job.Removed, job.Issue = true, &jobs.JobIssue{Code: "StorageOffline"}
	job.ActivityIntent = jobs.ActivityStopped
	if _, err := repository.SaveCAS(job, token); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(job.TargetDir, job.TargetDir+"-moved"); err != nil {
		t.Fatal(err)
	}
	check := taskResult(t, inspectTasks(t, application, repository, true), job.ID)
	if check.Code != "RemovalFailed" || strings.Contains(strings.Join(check.Recovery, " "), "Retry") || !strings.Contains(check.Explanation, "actions: remove") {
		t.Fatalf("invalid removal recovery: %+v", check)
	}
	if strings.Contains(check.Evidence, "target") {
		t.Fatalf("removal inspected final target: %+v", check)
	}
}

func TestDoctorReportsDuplicateBindingsAndNativeFailures(t *testing.T) {
	application, repository, rpc, job := doctorFixture(t)
	clone := job
	clone.ID = "abababababababab"
	if _, err := repository.Create(clone); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{job.ID, clone.ID} {
		if check := taskResult(t, inspectTasks(t, application, repository, true), id); check.Code != "ManagedIdentityConflict" {
			t.Fatalf("duplicate binding ignored: %+v", check)
		}
	}
	// Exercise a list larger than the Dashboard's managed observation capacity.
	for i := 0; i < 350; i++ {
		gid := fmt.Sprintf("%016x", i+100)
		rpc.statuses[gid] = aria2.LifecycleStatus{GID: gid, Status: "paused"}
	}
	rpc.statuses["eeeeeeeeeeeeeeee"] = aria2.LifecycleStatus{GID: "eeeeeeeeeeeeeeee", Status: "error", ErrorCode: "3", ErrorMessage: "resource not found"}
	report := inspectTasks(t, application, repository, true)
	if check := taskResult(t, report, "eeeeeeeeeeeeeeee"); check.Code != "NativeTaskError" || !strings.Contains(check.Evidence, "error=3") {
		t.Fatalf("native failure omitted: %+v", check)
	}
	if check := taskResult(t, report, fmt.Sprintf("%016x", 449)); check.TaskStatus != "paused" {
		t.Fatalf("task coverage truncated: %+v", check)
	}
}

func TestDoctorPublishedPayloadAndDetachedCompletion(t *testing.T) {
	application, repository, rpc, job := doctorFixture(t)
	job, token, _ := repository.Load(job.ID)
	path := filepath.Join(job.TargetDir, "payload.bin")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := publication.Identify(path)
	if err != nil {
		t.Fatal(err)
	}
	delete(rpc.statuses, job.Execution.GID)
	job.Execution = nil
	job.ActivityIntent = jobs.ActivityStopped
	job.Payload = jobs.PayloadState{Location: jobs.PayloadPublished, Root: "payload.bin", FinalRoot: "payload.bin", Identity: jobIdentity(identity)}
	if _, err := repository.SaveCAS(job, token); err != nil {
		t.Fatal(err)
	}
	check := taskResult(t, inspectTasks(t, application, repository, true), job.ID)
	if check.TaskStatus != "complete" || check.Severity != doctor.OK {
		t.Fatalf("detached completion flagged: %+v", check)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	check = taskResult(t, inspectTasks(t, application, repository, true), job.ID)
	if check.Code != "FinalSeedPathMismatch" {
		t.Fatalf("missing published payload ignored: %+v", check)
	}
}

func TestDoctorOfflineWarningDoesNotBecomeMissingExecution(t *testing.T) {
	application, repository, _, job := doctorFixture(t)
	job, token, _ := repository.Load(job.ID)
	job.Issue = &jobs.JobIssue{Code: "PowerLossDurabilityUnavailable"}
	if _, err := repository.SaveCAS(job, token); err != nil {
		t.Fatal(err)
	}
	check := taskResult(t, inspectTasks(t, application, repository, false), job.ID)
	if check.Code != "PowerLossDurabilityUnavailable" || check.Severity != doctor.Warning || check.TaskStatus != "unknown" {
		t.Fatalf("offline warning became execution failure: %+v", check)
	}
}

func TestDoctorReportsPublicationRecoveryWithoutMutatingJob(t *testing.T) {
	application, repository, rpc, job := doctorFixture(t)
	job, token, _ := repository.Load(job.ID)
	delete(rpc.statuses, job.Execution.GID)
	job.Execution = nil
	job.Payload.Root, job.Payload.FinalRoot = "payload.bin", "payload.bin"
	job.Payload.Identity = job.TargetIdentity
	if _, err := repository.SaveCAS(job, token); err != nil {
		t.Fatal(err)
	}
	before, beforeToken, _ := repository.Load(job.ID)
	check := taskResult(t, inspectTasks(t, application, repository, true), job.ID)
	if check.Code != "PublicationRecoveryRequired" {
		t.Fatalf("detached publication ignored: %+v", check)
	}
	after, afterToken, _ := repository.Load(job.ID)
	if !reflect.DeepEqual(before, after) || beforeToken != afterToken {
		t.Fatal("diagnosis changed prepared publication")
	}
}
