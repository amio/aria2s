package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/jobs"
	"github.com/amio/aria2s/internal/paths"
	"github.com/amio/aria2s/internal/publication"
	"github.com/amio/aria2s/internal/state"
)

type reconcilerRPC struct {
	repository   *jobs.Repository
	statuses     map[string]aria2.LifecycleStatus
	unknownAdd   bool
	bindingSeen  bool
	addGIDs      []string
	pauseCalls   []string
	resumeCalls  []string
	forceCalls   []string
	removeCalls  []string
	torrentFiles []aria2.DownloadFile
	torrentErr   error
	saveCalls    int
	saveErr      error
}

func (*reconcilerRPC) Version(context.Context, state.State) (string, error) { return "1.37.0", nil }
func (rpc *reconcilerRPC) AddURI(_ context.Context, _ state.State, _ string, options aria2.AddOptions) (string, error) {
	rpc.observePersistedBinding(options.GID)
	rpc.addGIDs = append(rpc.addGIDs, options.GID)
	rpc.statuses[options.GID] = aria2.LifecycleStatus{GID: options.GID, Status: "active", Dir: options.Dir}
	if rpc.unknownAdd {
		return "", &aria2.OutcomeUnknownError{Method: "aria2.addUri", Cause: context.DeadlineExceeded}
	}
	return options.GID, nil
}
func (rpc *reconcilerRPC) AddTorrent(_ context.Context, _ state.State, _ []byte, options aria2.AddOptions) (string, error) {
	rpc.observePersistedBinding(options.GID)
	rpc.addGIDs = append(rpc.addGIDs, options.GID)
	if rpc.torrentErr != nil {
		return "", rpc.torrentErr
	}
	rpc.statuses[options.GID] = aria2.LifecycleStatus{GID: options.GID, Status: "active", Dir: options.Dir, Files: rpc.torrentFiles, InfoHash: "0123456789012345678901234567890123456789"}
	return options.GID, nil
}

func TestDescriptorPromotionRetiresOldBindingAndStartsFreshTransfer(t *testing.T) {
	application, repository, rpc, target := newReconcilerTestApp(t)
	result, err := application.AddManaged(context.Background(), AddRequest{Source: "https://example.test/x.torrent", TargetDir: target})
	if err != nil {
		t.Fatal(err)
	}
	job, _, _ := repository.Load(result.Task.JobID)
	transferGID := job.Execution.GID
	scope, _ := repository.LoadStorage(job.StorageID)
	workDir := jobs.WorkDir(scope, job.ID)
	metainfo := []byte("d4:infod6:lengthi1e4:name1:x12:piece lengthi1e6:pieces20:01234567890123456789ee")
	descriptor := filepath.Join(workDir, "x.torrent")
	if err := os.WriteFile(descriptor, metainfo, 0o600); err != nil {
		t.Fatal(err)
	}
	rpc.statuses[transferGID] = aria2.LifecycleStatus{GID: transferGID, Status: "complete", Dir: workDir, CompletedLength: int64(len(metainfo)), TotalLength: int64(len(metainfo)), Files: []aria2.DownloadFile{{Path: descriptor}}}
	if _, err := application.ReconcileJob(context.Background(), job.ID, ReconcileInput{Mode: ReconcileLive}); err != nil {
		t.Fatal(err)
	}
	loaded, _, _ := repository.Load(job.ID)
	if loaded.Payload.Location != jobs.PayloadStaging || loaded.Payload.FinalRoot != "" || loaded.Execution == nil || loaded.Execution.GID == transferGID {
		t.Fatalf("descriptor was published or binding was reused: %+v", loaded)
	}
	if len(rpc.addGIDs) < 2 || rpc.addGIDs[len(rpc.addGIDs)-1] != loaded.Execution.GID {
		t.Fatalf("promoted transfer was not added with its new binding: %v", rpc.addGIDs)
	}
}

func TestMagnetMetadataPromotionRetiresOldBindingAndStartsFreshTransfer(t *testing.T) {
	application, repository, rpc, target := newReconcilerTestApp(t)
	result, err := application.AddManaged(context.Background(), AddRequest{Source: "magnet:?xt=urn:btih:test", TargetDir: target})
	if err != nil {
		t.Fatal(err)
	}
	job, _, _ := repository.Load(result.Task.JobID)
	metadataGID := job.Execution.GID
	scope, _ := repository.LoadStorage(job.StorageID)
	workDir := jobs.WorkDir(scope, job.ID)
	metainfo := []byte("d4:infod6:lengthi1e4:name1:x12:piece lengthi1e6:pieces20:01234567890123456789ee")
	infoHash, err := aria2.ValidateMetainfo(metainfo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, infoHash+".torrent"), metainfo, 0o600); err != nil {
		t.Fatal(err)
	}
	rpc.statuses[metadataGID] = aria2.LifecycleStatus{
		GID: metadataGID, Status: "active", Dir: workDir, InfoHash: infoHash,
		Files: []aria2.DownloadFile{{Path: "[METADATA]" + infoHash}},
	}
	if _, err := application.ReconcileJob(context.Background(), job.ID, ReconcileInput{Mode: ReconcileLive}); err != nil {
		t.Fatal(err)
	}
	loaded, _, _ := repository.Load(job.ID)
	if loaded.Payload.Location != jobs.PayloadStaging || loaded.Execution == nil || loaded.Execution.GID == metadataGID {
		t.Fatalf("metadata execution was published or reused: %+v", loaded)
	}
	if _, err := repository.ReadMetainfo(job.ID); err != nil {
		t.Fatalf("resolved metadata was not retained: %v", err)
	}
}

func TestPausedMetadataResumesBeforeSavedDescriptorAppears(t *testing.T) {
	application, repository, rpc, target := newReconcilerTestApp(t)
	result, err := application.AddManaged(context.Background(), AddRequest{Source: "magnet:?xt=urn:btih:test", TargetDir: target})
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := repository.Load(result.Task.JobID)
	if err != nil {
		t.Fatal(err)
	}
	metadataGID := job.Execution.GID
	if err := application.SetActivity(context.Background(), job.ID, false); err != nil {
		t.Fatal(err)
	}

	metainfo := []byte("d4:infod6:lengthi1e4:name1:x12:piece lengthi1e6:pieces20:01234567890123456789ee")
	infoHash, err := aria2.ValidateMetainfo(metainfo)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := repository.LoadStorage(job.StorageID)
	if err != nil {
		t.Fatal(err)
	}
	workDir := jobs.WorkDir(scope, job.ID)
	rpc.statuses[metadataGID] = aria2.LifecycleStatus{
		GID: metadataGID, Status: "paused", Dir: workDir, InfoHash: infoHash,
		Files: []aria2.DownloadFile{{Path: "[METADATA]" + infoHash}},
	}

	if err := application.SetActivity(context.Background(), job.ID, true); err != nil {
		t.Fatalf("Resume rejected metadata before aria2 saved its descriptor: %v", err)
	}
	resumed, _, err := repository.Load(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Execution == nil || resumed.Execution.GID != metadataGID || resumed.ActivityIntent != jobs.ActivityRunning || resumed.Issue != nil {
		t.Fatalf("pending metadata was promoted or left unhealthy: %+v", resumed)
	}
	if len(rpc.resumeCalls) != 1 || rpc.resumeCalls[0] != metadataGID {
		t.Fatalf("metadata execution was not resumed: %v", rpc.resumeCalls)
	}

	if err := os.WriteFile(filepath.Join(workDir, infoHash+".torrent"), metainfo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := application.ReconcileJob(context.Background(), job.ID, ReconcileInput{Mode: ReconcileLive}); err != nil {
		t.Fatal(err)
	}
	promoted, _, err := repository.Load(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if promoted.Execution == nil || promoted.Execution.GID == metadataGID {
		t.Fatalf("saved metadata descriptor was not promoted: %+v", promoted)
	}
	if _, err := repository.ReadMetainfo(job.ID); err != nil {
		t.Fatalf("promoted metadata was not retained: %v", err)
	}
}

func TestMetadataDescriptorMayBePendingUntilNativeCompletion(t *testing.T) {
	workDir := t.TempDir()
	for _, status := range []string{"active", "waiting", "paused"} {
		t.Run(status, func(t *testing.T) {
			_, promoted, err := completedDescriptor(workDir, aria2.LifecycleStatus{
				Status: status, InfoHash: "0123456789012345678901234567890123456789",
				Files: []aria2.DownloadFile{{Path: "[METADATA]pending"}},
			})
			if err != nil || promoted {
				t.Fatalf("pending metadata descriptor = promoted %t, error %v", promoted, err)
			}
		})
	}
	_, _, err := completedDescriptor(workDir, aria2.LifecycleStatus{
		Status: "complete", InfoHash: "0123456789012345678901234567890123456789",
		Files: []aria2.DownloadFile{{Path: "[METADATA]missing"}},
	})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed metadata did not require its descriptor: %v", err)
	}
}

func TestTransferPublicationAndFinalSeedUseDifferentExecutionGIDs(t *testing.T) {
	application, repository, rpc, target := newReconcilerTestApp(t)
	targetFact, _ := publication.InspectTarget(target)
	scope, _ := ensureStorageScope(repository, targetFact)
	jobID, transferGID := "8888888888888888", "9999999999999999"
	workDir := jobs.WorkDir(scope, jobID)
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(workDir, "x")
	if err := os.WriteFile(payload, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	job := jobs.Job{ID: jobID, Source: "magnet:?xt=urn:btih:test", TargetDir: target, TargetIdentity: jobIdentity(targetFact.Identity), StorageID: scope.ID, ActivityIntent: jobs.ActivityRunning, Payload: jobs.PayloadState{Location: jobs.PayloadStaging}, Execution: &jobs.ExecutionBinding{GID: transferGID}}
	if _, err := repository.Create(job); err != nil {
		t.Fatal(err)
	}
	metainfo := []byte("d4:infod6:lengthi1e4:name1:x12:piece lengthi1e6:pieces20:01234567890123456789ee")
	if err := repository.WriteMetainfo(jobID, metainfo); err != nil {
		t.Fatal(err)
	}
	rpc.statuses[transferGID] = aria2.LifecycleStatus{GID: transferGID, Status: "active", Dir: workDir, InfoHash: "0123456789012345678901234567890123456789", Seeder: true, CompletedLength: 1, TotalLength: 1, Files: []aria2.DownloadFile{{Path: payload, Length: 1, CompletedLength: 1}}}
	rpc.torrentFiles = []aria2.DownloadFile{{Path: filepath.Join(target, "x"), Length: 1, CompletedLength: 1}}
	if _, err := application.ReconcileJob(context.Background(), jobID, ReconcileInput{Mode: ReconcileLive}); err != nil {
		t.Fatal(err)
	}
	loaded, _, _ := repository.Load(jobID)
	if loaded.Payload.Location != jobs.PayloadPublished || loaded.Execution == nil || loaded.Execution.GID == transferGID {
		t.Fatalf("publication/seed binding = %+v", loaded)
	}
	if _, err := os.Stat(filepath.Join(target, "x")); err != nil {
		t.Fatalf("payload was not published: %v", err)
	}
}

func TestPublicationAutoSuffixesObservedConflict(t *testing.T) {
	application, repository, rpc, target := newReconcilerTestApp(t)
	targetFact, _ := publication.InspectTarget(target)
	scope, _ := ensureStorageScope(repository, targetFact)
	jobID, gid := "8989898989898989", "9090909090909090"
	workDir := jobs.WorkDir(scope, jobID)
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(workDir, "x")
	if err := os.WriteFile(source, []byte("managed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "x"), []byte("external"), 0o600); err != nil {
		t.Fatal(err)
	}
	job := jobs.Job{ID: jobID, Source: "https://example.test/x", TargetDir: target, TargetIdentity: jobIdentity(targetFact.Identity), StorageID: scope.ID, ActivityIntent: jobs.ActivityRunning, Payload: jobs.PayloadState{Location: jobs.PayloadStaging}, Execution: &jobs.ExecutionBinding{GID: gid}}
	if _, err := repository.Create(job); err != nil {
		t.Fatal(err)
	}
	rpc.statuses[gid] = aria2.LifecycleStatus{GID: gid, Status: "complete", Dir: workDir, CompletedLength: 7, TotalLength: 7, Files: []aria2.DownloadFile{{Path: source, Length: 7, CompletedLength: 7}}}
	if _, err := application.ReconcileJob(context.Background(), job.ID, ReconcileInput{Mode: ReconcileLive}); err != nil {
		t.Fatal(err)
	}
	loaded, _, _ := repository.Load(job.ID)
	if loaded.Payload.Location != jobs.PayloadPublished || loaded.Payload.FinalRoot != "x (1)" {
		t.Fatalf("conflict allocation = %+v", loaded.Payload)
	}
	data, _ := os.ReadFile(filepath.Join(target, "x"))
	if string(data) != "external" {
		t.Fatalf("external destination was overwritten: %q", data)
	}
}

func TestFinalSeedFailureDoesNotUndoPublishedPayload(t *testing.T) {
	application, repository, rpc, target := newReconcilerTestApp(t)
	targetFact, _ := publication.InspectTarget(target)
	scope, _ := ensureStorageScope(repository, targetFact)
	payload := filepath.Join(target, "x")
	if err := os.WriteFile(payload, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, _ := publication.Identify(payload)
	job := jobs.Job{ID: "aaaaaaaaaaaaaaaa", Source: "magnet:?xt=urn:btih:test", TargetDir: target, TargetIdentity: jobIdentity(targetFact.Identity), StorageID: scope.ID, ActivityIntent: jobs.ActivityRunning, Payload: jobs.PayloadState{Location: jobs.PayloadPublished, Root: "x", FinalRoot: "x", Identity: jobIdentity(identity)}}
	if _, err := repository.Create(job); err != nil {
		t.Fatal(err)
	}
	if err := repository.WriteMetainfo(job.ID, []byte("d4:infod6:lengthi1e4:name1:x12:piece lengthi1e6:pieces20:01234567890123456789ee")); err != nil {
		t.Fatal(err)
	}
	rpc.torrentErr = errors.New("seed unavailable")
	if _, err := application.ReconcileJob(context.Background(), job.ID, ReconcileInput{Mode: ReconcileLive}); err == nil {
		t.Fatal("expected actionable seed failure")
	}
	loaded, _, _ := repository.Load(job.ID)
	if loaded.Payload.Location != jobs.PayloadPublished || loaded.Issue == nil || loaded.Issue.Code != "FinalSeedStartFailed" {
		t.Fatalf("seed failure invalidated publication: %+v", loaded)
	}
	if _, err := os.Stat(payload); err != nil {
		t.Fatalf("published payload was lost: %v", err)
	}
}

func TestMissingPublishedSeedExposesActionableUserMessageWithoutLosingCause(t *testing.T) {
	application, repository, _, target := newReconcilerTestApp(t)
	targetFact, _ := publication.InspectTarget(target)
	scope, _ := ensureStorageScope(repository, targetFact)
	payload := filepath.Join(target, "moved")
	if err := os.WriteFile(payload, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := publication.Identify(payload)
	if err != nil {
		t.Fatal(err)
	}
	job := jobs.Job{
		ID: "a1a1a1a1a1a1a1a1", Source: "magnet:?xt=urn:btih:test", TargetDir: target,
		TargetIdentity: jobIdentity(targetFact.Identity), StorageID: scope.ID,
		ActivityIntent: jobs.ActivityRunning,
		Payload:        jobs.PayloadState{Location: jobs.PayloadPublished, Root: "moved", FinalRoot: "moved", Identity: jobIdentity(identity)},
	}
	if _, err := repository.Create(job); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(payload); err != nil {
		t.Fatal(err)
	}

	_, err = application.ReconcileJob(context.Background(), job.ID, ReconcileInput{Mode: ReconcileLive})
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing payload cause was not preserved: %v", err)
	}
	var userMessage interface{ UserMessage() string }
	if !errors.As(err, &userMessage) {
		t.Fatalf("missing payload error has no user message: %T", err)
	}
	want := "seed files are missing or changed; restore them to the download location and retry, or remove the task"
	if got := userMessage.UserMessage(); got != want {
		t.Fatalf("user message = %q, want %q", got, want)
	}
	loaded, _, loadErr := repository.Load(job.ID)
	if loadErr != nil || loaded.Issue == nil || loaded.Issue.Code != "FinalSeedPathMismatch" {
		t.Fatalf("missing payload issue was not persisted: job=%+v err=%v", loaded, loadErr)
	}
}

func TestCleanupFailureAfterPublicationIsOnlyAWarning(t *testing.T) {
	application, repository, rpc, target := newReconcilerTestApp(t)
	targetFact, _ := publication.InspectTarget(target)
	scope, _ := ensureStorageScope(repository, targetFact)
	payload := filepath.Join(target, "x")
	if err := os.WriteFile(payload, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, _ := publication.Identify(payload)
	job := jobs.Job{ID: "abababababababab", Source: "https://example.test/x", TargetDir: target, TargetIdentity: jobIdentity(targetFact.Identity), StorageID: scope.ID, ActivityIntent: jobs.ActivityStopped, Payload: jobs.PayloadState{Location: jobs.PayloadPublished, Root: "x", FinalRoot: "x", Identity: jobIdentity(identity)}}
	if _, err := repository.Create(job); err != nil {
		t.Fatal(err)
	}
	workDir := jobs.WorkDir(scope, job.ID)
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "unexpected-user-file"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := application.ReconcileJob(context.Background(), job.ID, ReconcileInput{Mode: ReconcileLive})
	if err != nil || result.Warning == nil {
		t.Fatalf("cleanup result=%+v err=%v", result, err)
	}
	loaded, _, _ := repository.Load(job.ID)
	if loaded.Payload.Location != jobs.PayloadPublished || loaded.Issue != nil {
		t.Fatalf("cleanup warning became durable failure: %+v", loaded)
	}
	if rpc.saveCalls != 1 {
		t.Fatalf("published state was not checkpointed before cleanup warning: calls=%d", rpc.saveCalls)
	}
}

func TestPreparedPublicationRetiresLiveAndStartupExecutionsBeforeRename(t *testing.T) {
	for _, mode := range []ReconcileMode{ReconcileLive, ReconcileStartup} {
		t.Run(string(mode), func(t *testing.T) {
			application, repository, rpc, target := newReconcilerTestApp(t)
			targetFact, _ := publication.InspectTarget(target)
			scope, _ := ensureStorageScope(repository, targetFact)
			jobID, gid := "cdcdcdcdcdcdcdcd", "efefefefefefefef"
			workDir := jobs.WorkDir(scope, jobID)
			if err := os.MkdirAll(workDir, 0o700); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(workDir, "x")
			if err := os.WriteFile(source, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			identity, _ := publication.Identify(source)
			job := jobs.Job{ID: jobID, Source: "https://example.test/x", TargetDir: target, TargetIdentity: jobIdentity(targetFact.Identity), StorageID: scope.ID, ActivityIntent: jobs.ActivityStopped, Payload: jobs.PayloadState{Location: jobs.PayloadStaging, Root: "x", FinalRoot: "x", Identity: jobIdentity(identity)}, Execution: &jobs.ExecutionBinding{GID: gid}}
			if _, err := repository.Create(job); err != nil {
				t.Fatal(err)
			}
			rpc.statuses[gid] = aria2.LifecycleStatus{GID: gid, Status: "active", Dir: workDir, Files: []aria2.DownloadFile{{Path: source}}}
			input := ReconcileInput{Mode: mode}
			if mode == ReconcileStartup {
				block := aria2.SessionBlock{URI: job.Source, Options: []aria2.SessionOption{{Key: "gid", Value: gid}, {Key: "dir", Value: workDir}}}
				input.SavedBlock = &block
			}
			if _, err := application.ReconcileJob(context.Background(), jobID, input); err != nil {
				t.Fatal(err)
			}
			loaded, _, _ := repository.Load(jobID)
			if loaded.Payload.Location != jobs.PayloadPublished || loaded.Execution != nil {
				t.Fatalf("prepared publication did not retire before commit: %+v", loaded)
			}
			if _, err := os.Stat(filepath.Join(target, "x")); err != nil {
				t.Fatalf("rename did not complete: %v", err)
			}
		})
	}
}
func (rpc *reconcilerRPC) observePersistedBinding(gid string) {
	if rpc.repository == nil {
		return
	}
	items, _ := rpc.repository.Scan()
	for _, item := range items {
		if item.Err == nil && item.Job.Execution != nil && item.Job.Execution.GID == gid {
			rpc.bindingSeen = true
		}
	}
}
func (rpc *reconcilerRPC) LifecycleStatus(_ context.Context, _ state.State, gid string) (aria2.LifecycleStatus, error) {
	status, ok := rpc.statuses[gid]
	if !ok {
		return aria2.LifecycleStatus{}, &aria2.RPCError{Method: "aria2.tellStatus", Code: 1, Message: "not found"}
	}
	return status, nil
}
func (rpc *reconcilerRPC) Pause(_ context.Context, _ state.State, gid string) error {
	rpc.pauseCalls = append(rpc.pauseCalls, gid)
	status := rpc.statuses[gid]
	status.Status = "paused"
	rpc.statuses[gid] = status
	return nil
}
func (rpc *reconcilerRPC) Resume(_ context.Context, _ state.State, gid string) error {
	rpc.resumeCalls = append(rpc.resumeCalls, gid)
	status := rpc.statuses[gid]
	status.Status = "active"
	rpc.statuses[gid] = status
	return nil
}
func (rpc *reconcilerRPC) ForceRemove(_ context.Context, _ state.State, gid string) error {
	rpc.forceCalls = append(rpc.forceCalls, gid)
	status := rpc.statuses[gid]
	status.Status = "removed"
	rpc.statuses[gid] = status
	return nil
}
func (rpc *reconcilerRPC) RemoveDownloadResult(_ context.Context, _ state.State, gid string) error {
	rpc.removeCalls = append(rpc.removeCalls, gid)
	delete(rpc.statuses, gid)
	return nil
}
func (rpc *reconcilerRPC) SaveSession(context.Context, state.State) error {
	rpc.saveCalls++
	return rpc.saveErr
}
func (*reconcilerRPC) Shutdown(context.Context, state.State) error { return nil }

func newReconcilerTestApp(t *testing.T) (*App, *jobs.Repository, *reconcilerRPC, string) {
	t.Helper()
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	if err := state.Save(servicePaths.StateFile, state.State{RuntimeSchemaVersion: 2, RPCPort: 6800, RPCSecret: "secret", SessionPath: servicePaths.SessionFile, StartupInputPath: servicePaths.StartupInputFile}); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "downloads")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	repository := jobs.New(servicePaths.StateDir)
	rpc := &reconcilerRPC{repository: repository, statuses: make(map[string]aria2.LifecycleStatus)}
	return New(Options{Paths: servicePaths, RPC: rpc, DownloadDir: target}), repository, rpc, target
}

func TestReconcilerPersistsDistinctBindingBeforeUnknownAddAndKeepsStableJobID(t *testing.T) {
	application, repository, rpc, target := newReconcilerTestApp(t)
	rpc.unknownAdd = true
	result, err := application.AddManaged(context.Background(), AddRequest{Source: "https://example.test/payload.bin", TargetDir: target})
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := repository.Load(result.Task.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if !rpc.bindingSeen || job.Execution == nil || job.Execution.GID == job.ID || job.Execution.GID != rpc.addGIDs[0] {
		t.Fatalf("job/binding were not durably separated before Add: job=%+v calls=%v", job, rpc.addGIDs)
	}
	if job.Issue != nil {
		t.Fatalf("observed unknown Add retained issue: %+v", job.Issue)
	}
}

func TestIntentEntryPointsMutateByJobIDAndRPCUsesExecutionGID(t *testing.T) {
	application, repository, rpc, target := newReconcilerTestApp(t)
	result, err := application.AddManaged(context.Background(), AddRequest{Source: "https://example.test/payload.bin", TargetDir: target})
	if err != nil {
		t.Fatal(err)
	}
	job, _, _ := repository.Load(result.Task.JobID)
	if err := application.SetActivity(context.Background(), job.ID, false); err != nil {
		t.Fatal(err)
	}
	if len(rpc.pauseCalls) != 1 || rpc.pauseCalls[0] != job.Execution.GID {
		t.Fatalf("pause did not map JobID to execution GID: %+v", rpc.pauseCalls)
	}
	loaded, _, _ := repository.Load(job.ID)
	if loaded.ID != job.ID || loaded.ActivityIntent != jobs.ActivityStopped {
		t.Fatalf("stable job identity or intent changed: %+v", loaded)
	}
}

func TestRemoveManagedPreservesNativeDisplayNameForHistory(t *testing.T) {
	application, repository, rpc, target := newReconcilerTestApp(t)
	result, err := application.AddManaged(context.Background(), AddRequest{
		Source:    "https://example.test/download?id=42",
		TargetDir: target,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := repository.Load(result.Task.JobID)
	if err != nil {
		t.Fatal(err)
	}
	native := rpc.statuses[job.Execution.GID]
	native.Name = "readable-release.iso"
	rpc.statuses[job.Execution.GID] = native

	if err := application.RemoveManaged(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	removed, _, err := repository.Load(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if removed.DisplayName != native.Name || !removed.Removed || removed.Execution != nil {
		t.Fatalf("removed manifest lost native display name: %+v", removed)
	}

	session := &DashboardSession{app: application}
	read := session.projectSnapshot(
		aria2.ReadBatch{},
		[]jobs.ScannedJob{{ID: removed.ID, Job: removed}},
		aria2.ReadBatchQuery{},
		"",
	)
	if len(read.Downloads.Stopped) != 1 || read.Downloads.Stopped[0].Name != native.Name ||
		read.Downloads.Stopped[0].CanonicalStatus != string(StatusRemoved) {
		t.Fatalf("removed history row = %#v", read.Downloads.Stopped)
	}
}

func TestRetryReplacesTerminalTransferExecution(t *testing.T) {
	application, repository, rpc, target := newReconcilerTestApp(t)
	result, err := application.AddManaged(context.Background(), AddRequest{Source: "https://example.test/payload.bin", TargetDir: target})
	if err != nil {
		t.Fatal(err)
	}
	job, _, _ := repository.Load(result.Task.JobID)
	oldGID := job.Execution.GID
	status := rpc.statuses[oldGID]
	status.Status = "error"
	rpc.statuses[oldGID] = status
	if err := application.RetryManaged(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	loaded, _, _ := repository.Load(job.ID)
	if loaded.Execution == nil || loaded.Execution.GID == oldGID {
		t.Fatalf("terminal execution was not replaced: %+v", loaded)
	}
}

func TestLiveRetiresAbsentLegacyJobIDBindingBeforeRestart(t *testing.T) {
	application, repository, _, target := newReconcilerTestApp(t)
	targetFact, _ := publication.InspectTarget(target)
	scope, _ := ensureStorageScope(repository, targetFact)
	job := jobs.Job{
		ID: "1414141414141414", Source: "https://example.test/x", TargetDir: target,
		TargetIdentity: jobIdentity(targetFact.Identity), StorageID: scope.ID,
		ActivityIntent: jobs.ActivityRunning, Payload: jobs.PayloadState{Location: jobs.PayloadStaging},
		Execution: &jobs.ExecutionBinding{GID: "1414141414141414"},
	}
	if _, err := repository.Create(job); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(jobs.WorkDir(scope, job.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := application.ReconcileJob(context.Background(), job.ID, ReconcileInput{Mode: ReconcileLive}); err != nil {
		t.Fatal(err)
	}
	loaded, _, _ := repository.Load(job.ID)
	if loaded.Execution == nil || loaded.Execution.GID == job.ID {
		t.Fatalf("absent legacy binding was reused: %+v", loaded)
	}
}

func TestStartupKeepsValidatedLegacyPendingSavedBinding(t *testing.T) {
	application, repository, _, target := newReconcilerTestApp(t)
	targetFact, _ := publication.InspectTarget(target)
	scope, _ := ensureStorageScope(repository, targetFact)
	const jobID = "1515151515151515"
	workDir := jobs.WorkDir(scope, jobID)
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := map[string]any{
		"version": 1, "id": jobID, "source": "https://example.test/x", "targetDir": target,
		"targetIdentity": jobIdentity(targetFact.Identity), "storageId": scope.ID,
		"phase": "pending", "activityIntent": "running",
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	manifestDir := filepath.Join(repository.Root(), "jobs", jobID)
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	block := aria2.SessionBlock{URI: "https://example.test/x", Options: []aria2.SessionOption{{Key: "gid", Value: jobID}, {Key: "dir", Value: workDir}}}
	result, err := application.ReconcileJob(context.Background(), jobID, ReconcileInput{Mode: ReconcileStartup, SavedBlock: &block})
	if err != nil {
		t.Fatal(err)
	}
	if result.StartupBlock == nil {
		t.Fatal("validated legacy pending saved block was discarded")
	}
	gid, _ := result.StartupBlock.Option("gid")
	if gid != jobID {
		t.Fatalf("validated legacy binding changed: %q", gid)
	}
}

func TestStartupReconstructionUsesFreshGIDAndForcesIntegrityWithoutControl(t *testing.T) {
	application, repository, _, target := newReconcilerTestApp(t)
	targetFact, err := publication.InspectTarget(target)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := ensureStorageScope(repository, targetFact)
	if err != nil {
		t.Fatal(err)
	}
	job := jobs.Job{ID: "1111111111111111", Source: "magnet:?xt=urn:btih:test", TargetDir: target, TargetIdentity: jobIdentity(targetFact.Identity), StorageID: scope.ID, ActivityIntent: jobs.ActivityRunning, Payload: jobs.PayloadState{Location: jobs.PayloadStaging, Root: "x"}, Execution: &jobs.ExecutionBinding{GID: "2222222222222222"}}
	if _, err := repository.Create(job); err != nil {
		t.Fatal(err)
	}
	metainfo := []byte("d4:infod6:lengthi1e4:name1:x12:piece lengthi1e6:pieces20:01234567890123456789ee")
	if err := repository.WriteMetainfo(job.ID, metainfo); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(jobs.WorkDir(scope, job.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobs.WorkDir(scope, job.ID), "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := application.ReconcileJob(context.Background(), job.ID, ReconcileInput{Mode: ReconcileStartup})
	if err != nil {
		t.Fatal(err)
	}
	if result.StartupBlock == nil {
		t.Fatal("safe torrent reconstruction was omitted")
	}
	gid, _ := result.StartupBlock.Option("gid")
	integrity, _ := result.StartupBlock.Option("check-integrity")
	if gid == job.Execution.GID || integrity != "true" {
		t.Fatalf("startup block gid=%q integrity=%q", gid, integrity)
	}
}

func TestPreparedPublicationCrashRecoveryCommitsWeakDestinationWithoutExecution(t *testing.T) {
	application, repository, _, target := newReconcilerTestApp(t)
	targetFact, err := publication.InspectTarget(target)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := ensureStorageScope(repository, targetFact)
	if err != nil {
		t.Fatal(err)
	}
	job := jobs.Job{ID: "3333333333333333", Source: "https://example.test/x", TargetDir: target, TargetIdentity: jobIdentity(targetFact.Identity), StorageID: scope.ID, ActivityIntent: jobs.ActivityStopped, Payload: jobs.PayloadState{Location: jobs.PayloadStaging, Root: "x", FinalRoot: "x", Identity: jobs.ObjectIdentity{MountID: targetFact.Identity.MountID, ObjectID: 99, ReliableAcrossRename: false}}}
	if _, err := repository.Create(job); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "x"), []byte("published"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := application.ReconcileJob(context.Background(), job.ID, ReconcileInput{Mode: ReconcileStartup}); err != nil {
		t.Fatal(err)
	}
	loaded, _, _ := repository.Load(job.ID)
	if loaded.Payload.Location != jobs.PayloadPublished || loaded.Execution != nil || loaded.Issue != nil {
		t.Fatalf("weak post-rename recovery did not commit: %+v", loaded)
	}
}

func TestPreparedPublicationCrashRecoveryRequiresReliableDestinationIdentity(t *testing.T) {
	application, repository, _, target := newReconcilerTestApp(t)
	targetFact, _ := publication.InspectTarget(target)
	scope, _ := ensureStorageScope(repository, targetFact)
	jobID := "3434343434343434"
	workDir := jobs.WorkDir(scope, jobID)
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(workDir, "x")
	if err := os.WriteFile(source, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, _ := publication.Identify(source)
	if !identity.ReliableAcrossRename {
		t.Skip("test filesystem does not expose reliable rename identity")
	}
	job := jobs.Job{ID: jobID, Source: "https://example.test/x", TargetDir: target, TargetIdentity: jobIdentity(targetFact.Identity), StorageID: scope.ID, ActivityIntent: jobs.ActivityStopped, Payload: jobs.PayloadState{Location: jobs.PayloadStaging, Root: "x", FinalRoot: "x", Identity: jobIdentity(identity)}}
	if _, err := repository.Create(job); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source, filepath.Join(target, "x")); err != nil {
		t.Fatal(err)
	}
	if _, err := application.ReconcileJob(context.Background(), job.ID, ReconcileInput{Mode: ReconcileStartup}); err != nil {
		t.Fatal(err)
	}
	loaded, _, _ := repository.Load(job.ID)
	if loaded.Payload.Location != jobs.PayloadPublished || loaded.Issue != nil {
		t.Fatalf("reliable post-rename recovery did not commit: %+v", loaded)
	}
}

func TestHookResolutionRejectsDuplicateAndIgnoresStaleGID(t *testing.T) {
	application, repository, _, target := newReconcilerTestApp(t)
	targetFact, _ := publication.InspectTarget(target)
	scope, _ := ensureStorageScope(repository, targetFact)
	for index, id := range []string{"4444444444444444", "5555555555555555"} {
		job := jobs.Job{ID: id, Source: "https://example.test/x", TargetDir: target, TargetIdentity: jobIdentity(targetFact.Identity), StorageID: scope.ID, ActivityIntent: jobs.ActivityRunning, Payload: jobs.PayloadState{Location: jobs.PayloadStaging}, Execution: &jobs.ExecutionBinding{GID: "6666666666666666"}}
		if _, err := repository.Create(job); err != nil {
			t.Fatalf("create duplicate %d: %v", index, err)
		}
	}
	oldCloser := closerInheritedLock
	closerInheritedLock = func() error { return nil }
	t.Cleanup(func() { closerInheritedLock = oldCloser })
	if err := application.ManagedHook(context.Background(), "on-download-complete", "7777777777777777"); err != nil {
		t.Fatalf("stale hook was not a no-op: %v", err)
	}
	if err := application.ManagedHook(context.Background(), "on-download-complete", "6666666666666666"); err == nil {
		t.Fatal("duplicate execution binding was accepted")
	}
	if _, err := application.ReconcileJob(context.Background(), "4444444444444444", ReconcileInput{Mode: ReconcileLive}); err == nil {
		t.Fatal("direct reconciliation accepted a duplicate execution binding")
	}
}

func TestSuccessfulAddStillConfirmsNativeDirectory(t *testing.T) {
	rpc := &reconcilerRPC{statuses: map[string]aria2.LifecycleStatus{
		"1111111111111111": {GID: "1111111111111111", Status: "active", Dir: "/wrong"},
	}}
	err := confirmManagedAdd(context.Background(), rpc, state.State{}, "1111111111111111", "/expected", "1111111111111111", nil)
	if err == nil || !strings.Contains(err.Error(), "ManagedIdentityConflict") {
		t.Fatalf("successful Add skipped native directory confirmation: %v", err)
	}
}

func TestRetryRemovedCleansBeforeRestarting(t *testing.T) {
	application, repository, _, target := newReconcilerTestApp(t)
	targetFact, _ := publication.InspectTarget(target)
	scope, _ := ensureStorageScope(repository, targetFact)
	job := jobs.Job{
		ID: "1212121212121212", Source: "https://example.test/x", TargetDir: target,
		TargetIdentity: jobIdentity(targetFact.Identity), StorageID: scope.ID,
		ActivityIntent: jobs.ActivityStopped, Removed: true,
		Payload: jobs.PayloadState{Location: jobs.PayloadStaging},
		Issue:   &jobs.JobIssue{Code: "CleanupFailed"},
	}
	if _, err := repository.Create(job); err != nil {
		t.Fatal(err)
	}
	workDir := jobs.WorkDir(scope, job.ID)
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(workDir, "stale-partial")
	if err := os.WriteFile(stale, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := application.RetryManaged(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	loaded, _, _ := repository.Load(job.ID)
	if loaded.Removed || loaded.Execution == nil || loaded.Issue != nil {
		t.Fatalf("removed Retry did not clean then restart: %+v", loaded)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale staging data survived removed Retry: %v", err)
	}
}

func TestRetryRemovedStagedJobAdoptsRecreatedTargetAfterCleanup(t *testing.T) {
	application, repository, _, target := newReconcilerTestApp(t)
	targetFact, _ := publication.InspectTarget(target)
	scope, _ := ensureStorageScope(repository, targetFact)
	job := jobs.Job{
		ID: "1616161616161616", Source: "https://example.test/x", TargetDir: targetFact.Path,
		TargetIdentity: jobIdentity(targetFact.Identity), StorageID: scope.ID,
		ActivityIntent: jobs.ActivityStopped, Removed: true,
		Payload: jobs.PayloadState{Location: jobs.PayloadStaging},
	}
	if _, err := repository.Create(job); err != nil {
		t.Fatal(err)
	}
	workDir := jobs.WorkDir(scope, job.ID)
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(workDir, "stale-partial")
	if err := os.WriteFile(stale, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target, target+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	replacement, err := publication.InspectTarget(target)
	if err != nil {
		t.Fatal(err)
	}

	if err := application.RetryManaged(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := repository.Load(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Removed || loaded.Execution == nil || loaded.TargetIdentity.ObjectID != replacement.Identity.ObjectID || loaded.Issue != nil {
		t.Fatalf("removed Retry did not prepare target recovery before convergence: %+v", loaded)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale staging data survived removed Retry: %v", err)
	}
}

func TestRetryCleanupFailureDoesNotReviveRemovedJob(t *testing.T) {
	application, repository, _, target := newReconcilerTestApp(t)
	targetFact, _ := publication.InspectTarget(target)
	scope, _ := ensureStorageScope(repository, targetFact)
	job := jobs.Job{
		ID: "1717171717171717", Source: "https://example.test/x", TargetDir: target,
		TargetIdentity: jobIdentity(targetFact.Identity), StorageID: scope.ID,
		ActivityIntent: jobs.ActivityStopped, Removed: true,
		Payload: jobs.PayloadState{Location: jobs.PayloadStaging},
	}
	if _, err := repository.Create(job); err != nil {
		t.Fatal(err)
	}
	registeredMarker := filepath.Join(scope.StagingAnchor, ".aria2s_staging", scope.ID)
	if err := os.Rename(registeredMarker, registeredMarker+"-replaced"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(registeredMarker, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := application.RetryManaged(context.Background(), job.ID); err == nil {
		t.Fatal("Retry revived a removed task after cleanup validation failed")
	}
	loaded, _, err := repository.Load(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Removed || loaded.Execution != nil || loaded.ActivityIntent != jobs.ActivityStopped || loaded.Issue == nil || loaded.Issue.Code != "StorageOffline" {
		t.Fatalf("failed cleanup changed removed intent: %+v", loaded)
	}
}

func TestRemoveManagedRetriesCleanupWithoutRestarting(t *testing.T) {
	application, repository, _, target := newReconcilerTestApp(t)
	targetFact, _ := publication.InspectTarget(target)
	scope, _ := ensureStorageScope(repository, targetFact)
	job := jobs.Job{
		ID: "1414141414141414", Source: "https://example.test/x", TargetDir: target,
		TargetIdentity: jobIdentity(targetFact.Identity), StorageID: scope.ID,
		ActivityIntent: jobs.ActivityStopped, Removed: true,
		Payload: jobs.PayloadState{Location: jobs.PayloadStaging},
		Issue:   &jobs.JobIssue{Code: "CleanupFailed"},
	}
	if _, err := repository.Create(job); err != nil {
		t.Fatal(err)
	}
	workDir := jobs.WorkDir(scope, job.ID)
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "stale-partial"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := application.RemoveManaged(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := repository.Load(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Removed || loaded.Execution != nil || loaded.Issue != nil {
		t.Fatalf("cleanup retry resurrected removed job: %+v", loaded)
	}
	if _, err := os.Stat(workDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging directory survived cleanup retry: %v", err)
	}

	if err := application.ClearManaged(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if repository.Exists(job.ID) {
		t.Fatal("removed job remained after Clear")
	}
}

func TestDeleteManagedRemovesNativeStagingAndManifest(t *testing.T) {
	application, repository, rpc, target := newReconcilerTestApp(t)
	result, err := application.AddManaged(context.Background(), AddRequest{
		Source:    "magnet:?xt=urn:btih:test",
		TargetDir: target,
	})
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
	workDir := jobs.WorkDir(scope, job.ID)

	if err := application.DeleteManaged(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if repository.Exists(job.ID) {
		t.Fatal("metadata manifest survived permanent delete")
	}
	if _, err := os.Stat(workDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("metadata staging directory survived permanent delete: %v", err)
	}
	if len(rpc.forceCalls) != 1 || rpc.forceCalls[0] != job.Execution.GID {
		t.Fatalf("metadata native execution was not detached: %v", rpc.forceCalls)
	}
	if len(rpc.removeCalls) != 1 || rpc.removeCalls[0] != job.Execution.GID {
		t.Fatalf("metadata native result was not cleared: %v", rpc.removeCalls)
	}
}

func TestDeleteManagedAfterTargetDirectoryRename(t *testing.T) {
	application, repository, rpc, target := newReconcilerTestApp(t)
	result, err := application.AddManaged(context.Background(), AddRequest{
		Source:    "magnet:?xt=urn:btih:test",
		TargetDir: target,
	})
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
	workDir := jobs.WorkDir(scope, job.ID)
	if err := os.Rename(target, target+"-renamed"); err != nil {
		t.Fatal(err)
	}
	if _, err := application.ReconcileJob(context.Background(), job.ID, ReconcileInput{Mode: ReconcileLive}); err == nil {
		t.Fatal("renamed target did not produce the expected storage issue")
	}
	blocked, _, err := repository.Load(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Issue == nil || blocked.Issue.Code != "StorageOffline" {
		t.Fatalf("renamed target issue = %+v, want StorageOffline", blocked.Issue)
	}

	if err := application.DeleteManaged(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if repository.Exists(job.ID) {
		t.Fatal("metadata manifest survived delete after target rename")
	}
	if _, err := os.Stat(workDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging directory survived delete after target rename: %v", err)
	}
	if len(rpc.forceCalls) != 1 || rpc.forceCalls[0] != job.Execution.GID {
		t.Fatalf("native execution was not detached after target rename: %v", rpc.forceCalls)
	}
}

func TestRetryAdoptsRecreatedTargetForStagedJob(t *testing.T) {
	application, repository, _, target := newReconcilerTestApp(t)
	result, err := application.AddManaged(context.Background(), AddRequest{
		Source:    "https://example.test/x",
		TargetDir: target,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := repository.Load(result.Task.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target, target+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	replacement, err := publication.InspectTarget(target)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Identity.ObjectID == job.TargetIdentity.ObjectID {
		t.Fatal("replacement unexpectedly retained the original target identity")
	}

	if _, err := application.ReconcileJob(context.Background(), job.ID, ReconcileInput{Mode: ReconcileLive}); err == nil {
		t.Fatal("ordinary reconciliation adopted a replacement target")
	}
	if err := application.RetryManaged(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := repository.Load(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TargetIdentity.ObjectID != replacement.Identity.ObjectID || loaded.Issue != nil {
		t.Fatalf("Retry did not adopt the recreated target: %+v", loaded)
	}
}

func TestRetryCreatesMissingTargetForStagedJob(t *testing.T) {
	application, repository, _, target := newReconcilerTestApp(t)
	result, err := application.AddManaged(context.Background(), AddRequest{
		Source:    "https://example.test/x",
		TargetDir: target,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := repository.Load(result.Task.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target, target+"-moved"); err != nil {
		t.Fatal(err)
	}

	if err := application.RetryManaged(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	recreated, err := publication.InspectTarget(target)
	if err != nil {
		t.Fatalf("Retry did not recreate the missing target: %v", err)
	}
	loaded, _, err := repository.Load(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TargetIdentity.ObjectID != recreated.Identity.ObjectID || loaded.Issue != nil {
		t.Fatalf("Retry did not adopt the recreated target: %+v", loaded)
	}
}

func TestRetryDoesNotCreateTargetThroughSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	realParent := filepath.Join(root, "real")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedParent := filepath.Join(root, "linked")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Fatal(err)
	}

	if _, err := retryTarget(jobs.StorageScope{}, filepath.Join(linkedParent, "downloads")); err == nil {
		t.Fatal("Retry created a target through a symlinked parent")
	}
	if _, err := os.Stat(filepath.Join(realParent, "downloads")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected target behind symlink: %v", err)
	}
}

func TestRetryDoesNotAdoptRecreatedTargetAfterPublication(t *testing.T) {
	application, repository, _, target := newReconcilerTestApp(t)
	targetFact, err := publication.InspectTarget(target)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := ensureStorageScope(repository, targetFact)
	if err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(target, "x")
	if err := os.WriteFile(payload, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := publication.Identify(payload)
	if err != nil {
		t.Fatal(err)
	}
	job := jobs.Job{
		ID: "1515151515151515", Source: "https://example.test/x", TargetDir: target,
		TargetIdentity: jobIdentity(targetFact.Identity), StorageID: scope.ID,
		ActivityIntent: jobs.ActivityStopped,
		Payload:        jobs.PayloadState{Location: jobs.PayloadPublished, Root: "x", FinalRoot: "x", Identity: jobIdentity(identity)},
	}
	if _, err := repository.Create(job); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target, target+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := application.RetryManaged(context.Background(), job.ID); err == nil {
		t.Fatal("Retry adopted a replacement target without its published payload")
	}
	loaded, _, err := repository.Load(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TargetIdentity.ObjectID != job.TargetIdentity.ObjectID || loaded.Issue == nil || loaded.Issue.Code != "StorageOffline" {
		t.Fatalf("published target replacement was not rejected: %+v", loaded)
	}
}

func TestRetryRemovedRenamedPublicationRemainsStopped(t *testing.T) {
	application, repository, _, target := newReconcilerTestApp(t)
	targetFact, _ := publication.InspectTarget(target)
	scope, _ := ensureStorageScope(repository, targetFact)
	payload := filepath.Join(target, "x (1)")
	if err := os.WriteFile(payload, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, _ := publication.Identify(payload)
	job := jobs.Job{
		ID: "1313131313131313", Source: "magnet:?xt=urn:btih:test", TargetDir: target,
		TargetIdentity: jobIdentity(targetFact.Identity), StorageID: scope.ID,
		ActivityIntent: jobs.ActivityStopped, Removed: true,
		Payload: jobs.PayloadState{Location: jobs.PayloadPublished, Root: "x", FinalRoot: "x (1)", Identity: jobIdentity(identity)},
	}
	if _, err := repository.Create(job); err != nil {
		t.Fatal(err)
	}
	if err := application.RetryManaged(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	loaded, _, _ := repository.Load(job.ID)
	if loaded.Removed || loaded.ActivityIntent != jobs.ActivityStopped || loaded.Execution != nil {
		t.Fatalf("renamed publication Retry attempted to seed: %+v", loaded)
	}
}
