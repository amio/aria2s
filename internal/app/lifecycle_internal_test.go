package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/jobs"
	"github.com/amio/aria2s/internal/paths"
	"github.com/amio/aria2s/internal/publication"
	"github.com/amio/aria2s/internal/state"
)

type lifecycleRPC struct {
	status           aria2.LifecycleStatus
	statusErr        error
	added            aria2.AddOptions
	census           []aria2.LifecycleStatus
	forceCalls       int
	clearCalls       int
	clearMakesAbsent bool
	addMakesVisible  bool
	torrentCalls     int
	torrentFiles     []aria2.DownloadFile
}

func (*lifecycleRPC) Version(context.Context, state.State) (string, error) { return "1.37.0", nil }
func (rpc *lifecycleRPC) AddURI(_ context.Context, _ state.State, _ string, options aria2.AddOptions) (string, error) {
	rpc.added = options
	if rpc.addMakesVisible {
		status := "active"
		if options.Pause {
			status = "paused"
		}
		rpc.status = aria2.LifecycleStatus{GID: options.GID, Status: status, Dir: options.Dir}
		rpc.statusErr = nil
	}
	return options.GID, nil
}
func (rpc *lifecycleRPC) AddTorrent(_ context.Context, _ state.State, _ []byte, options aria2.AddOptions) (string, error) {
	rpc.torrentCalls++
	if rpc.addMakesVisible {
		rpc.status = aria2.LifecycleStatus{GID: options.GID, Status: "active", Dir: options.Dir, Files: rpc.torrentFiles}
		rpc.statusErr = nil
	}
	return options.GID, nil
}
func (rpc *lifecycleRPC) LifecycleStatus(context.Context, state.State, string) (aria2.LifecycleStatus, error) {
	return rpc.status, rpc.statusErr
}
func (*lifecycleRPC) Pause(context.Context, state.State, string) error  { return nil }
func (*lifecycleRPC) Resume(context.Context, state.State, string) error { return nil }
func (rpc *lifecycleRPC) ForceRemove(context.Context, state.State, string) error {
	rpc.forceCalls++
	return nil
}
func (rpc *lifecycleRPC) RemoveDownloadResult(context.Context, state.State, string) error {
	rpc.clearCalls++
	if rpc.clearMakesAbsent {
		rpc.status, rpc.statusErr = aria2.LifecycleStatus{}, &aria2.RPCError{Method: "aria2.tellStatus", Code: 1, Message: "not found"}
	}
	return nil
}
func (*lifecycleRPC) SaveSession(context.Context, state.State) error { return nil }
func (*lifecycleRPC) Shutdown(context.Context, state.State) error    { return nil }
func (rpc *lifecycleRPC) CompleteCensus(context.Context, state.State) ([]aria2.LifecycleStatus, error) {
	return rpc.census, nil
}

func TestManagedHTTPAddAndPublicationKeepTargetCleanUntilAtomicRoot(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	target := filepath.Join(root, "downloads")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	current := state.State{RuntimeSchemaVersion: 2, RPCPort: 6800, RPCSecret: "secret", SessionPath: servicePaths.SessionFile, StartupInputPath: servicePaths.StartupInputFile}
	if err := state.Save(servicePaths.StateFile, current); err != nil {
		t.Fatal(err)
	}
	rpc := &lifecycleRPC{clearMakesAbsent: true}
	application := New(Options{Paths: servicePaths, RPC: rpc, DownloadDir: target})
	result, err := application.AddManaged(context.Background(), AddRequest{Source: "https://example.test/payload.bin", TargetDir: target})
	if err != nil {
		t.Fatal(err)
	}
	if rpc.added.GID != result.Task.GID || !rpc.added.Managed {
		t.Fatalf("managed options = %+v", rpc.added)
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Fatalf("target before completion = %v err=%v", entries, err)
	}
	repository := jobs.New(servicePaths.StateDir)
	job, _, err := repository.Load(result.Task.GID)
	if err != nil || job.Phase != jobs.PhaseStaged {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	scope, err := repository.LoadStorage(job.StorageID)
	if err != nil {
		t.Fatal(err)
	}
	work := jobs.WorkDir(scope, job.ID)
	payload := filepath.Join(work, "payload.bin")
	if err := os.WriteFile(payload, []byte("complete"), 0o600); err != nil {
		t.Fatal(err)
	}
	rpc.status = aria2.LifecycleStatus{GID: job.ID, Status: "complete", Dir: work, CompletedLength: 8, TotalLength: 8, Files: []aria2.DownloadFile{{Path: payload, Name: "payload.bin", Length: 8, CompletedLength: 8}}}
	if err := application.ManagedHook(context.Background(), "on-download-complete", job.ID); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(target, "payload.bin"))
	if err != nil || string(data) != "complete" {
		t.Fatalf("published data=%q err=%v", data, err)
	}
	job, _, err = repository.Load(job.ID)
	if err != nil || job.Phase != jobs.PhasePublished || job.ActivityIntent != jobs.ActivityStopped {
		t.Fatalf("published job=%+v err=%v", job, err)
	}
	if err := application.ManagedHook(context.Background(), "on-download-complete", job.ID); err != nil {
		t.Fatalf("duplicate completion hook was not idempotent: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(target, "payload.bin")); err != nil || string(data) != "complete" {
		t.Fatalf("duplicate hook changed published payload: %q err=%v", data, err)
	}
}

func TestManagedHookRejectsIncompleteCompletionEvent(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	target := filepath.Join(root, "downloads")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	current := state.State{RuntimeSchemaVersion: 2, RPCPort: 6800, RPCSecret: "secret", SessionPath: servicePaths.SessionFile, StartupInputPath: servicePaths.StartupInputFile}
	if err := state.Save(servicePaths.StateFile, current); err != nil {
		t.Fatal(err)
	}
	rpc := &lifecycleRPC{}
	application := New(Options{Paths: servicePaths, RPC: rpc, DownloadDir: target})
	result, err := application.AddManaged(context.Background(), AddRequest{Source: "https://example.test/payload.bin", TargetDir: target})
	if err != nil {
		t.Fatal(err)
	}
	repository := jobs.New(servicePaths.StateDir)
	job, _, err := repository.Load(result.Task.GID)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := repository.LoadStorage(job.StorageID)
	if err != nil {
		t.Fatal(err)
	}
	work := jobs.WorkDir(scope, job.ID)
	payload := filepath.Join(work, "payload.bin")
	if err := os.WriteFile(payload, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	rpc.status = aria2.LifecycleStatus{GID: job.ID, Status: "complete", Dir: work, CompletedLength: 7, TotalLength: 8, Files: []aria2.DownloadFile{{Path: payload, Length: 8, CompletedLength: 7}}}
	if err := application.ManagedHook(context.Background(), "on-download-complete", job.ID); err == nil {
		t.Fatal("incomplete completion event published payload")
	}
	if _, err := os.Stat(filepath.Join(target, "payload.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target changed after rejected event: %v", err)
	}
	job, _, err = repository.Load(job.ID)
	if err != nil || job.Phase != jobs.PhaseStaged {
		t.Fatalf("job after rejected event = %+v err=%v", job, err)
	}
}

func TestRemoveCompletedResultSkipsForceRemoveAndCleansStaging(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	target := filepath.Join(root, "downloads")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	current := state.State{RuntimeSchemaVersion: 2, RPCPort: 6800, RPCSecret: "secret", SessionPath: servicePaths.SessionFile, StartupInputPath: servicePaths.StartupInputFile}
	if err := state.Save(servicePaths.StateFile, current); err != nil {
		t.Fatal(err)
	}
	rpc := &lifecycleRPC{clearMakesAbsent: true}
	application := New(Options{Paths: servicePaths, RPC: rpc, DownloadDir: target})
	result, err := application.AddManaged(context.Background(), AddRequest{Source: "https://example.test/payload.bin", TargetDir: target})
	if err != nil {
		t.Fatal(err)
	}
	repository := jobs.New(servicePaths.StateDir)
	job, _, err := repository.Load(result.Task.GID)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := repository.LoadStorage(job.StorageID)
	if err != nil {
		t.Fatal(err)
	}
	work := jobs.WorkDir(scope, job.ID)
	if err := os.WriteFile(filepath.Join(work, "partial.bin"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	rpc.status = aria2.LifecycleStatus{GID: job.ID, Status: "complete", Dir: work}
	if err := application.RemoveManaged(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if rpc.forceCalls != 0 || rpc.clearCalls != 1 {
		t.Fatalf("remove calls: force=%d clear=%d", rpc.forceCalls, rpc.clearCalls)
	}
	if _, err := os.Stat(work); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging survived remove: %v", err)
	}
}

func TestRemovedRetryConvergesCrashAfterTombstone(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	target := filepath.Join(root, "downloads")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	current := state.State{RuntimeSchemaVersion: 2, RPCPort: 6800, RPCSecret: "secret", SessionPath: servicePaths.SessionFile, StartupInputPath: servicePaths.StartupInputFile}
	if err := state.Save(servicePaths.StateFile, current); err != nil {
		t.Fatal(err)
	}
	rpc := &lifecycleRPC{statusErr: &aria2.RPCError{Method: "aria2.tellStatus", Code: 1, Message: "not found"}}
	application := New(Options{Paths: servicePaths, RPC: rpc, DownloadDir: target})
	result, err := application.AddManaged(context.Background(), AddRequest{Source: "https://example.test/payload.bin", TargetDir: target})
	if err != nil {
		t.Fatal(err)
	}
	repository := jobs.New(servicePaths.StateDir)
	job, token, err := repository.Load(result.Task.GID)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := repository.LoadStorage(job.StorageID)
	if err != nil {
		t.Fatal(err)
	}
	work := jobs.WorkDir(scope, job.ID)
	if err := os.WriteFile(filepath.Join(work, "partial.bin"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	job.Phase, job.ActivityIntent = jobs.PhaseRemoved, jobs.ActivityStopped
	if _, err := repository.SaveCAS(job, token); err != nil {
		t.Fatal(err)
	}
	if err := application.RetryManaged(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(work); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging survived removed Retry: %v", err)
	}
	if err := application.ClearManaged(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRemovedRetryConvergesCrashBeforeNativeDetach(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	target := filepath.Join(root, "downloads")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := state.Save(servicePaths.StateFile, state.State{RuntimeSchemaVersion: 2}); err != nil {
		t.Fatal(err)
	}
	rpc := &lifecycleRPC{clearMakesAbsent: true}
	application := New(Options{Paths: servicePaths, RPC: rpc})
	result, err := application.AddManaged(context.Background(), AddRequest{Source: "https://example.test/payload.bin", TargetDir: target})
	if err != nil {
		t.Fatal(err)
	}
	repository := jobs.New(servicePaths.StateDir)
	job, token, err := repository.Load(result.Task.GID)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := repository.LoadStorage(job.StorageID)
	if err != nil {
		t.Fatal(err)
	}
	workDir := jobs.WorkDir(scope, job.ID)
	if err := os.WriteFile(filepath.Join(workDir, "partial.bin"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	job.Phase, job.ActivityIntent = jobs.PhaseRemoved, jobs.ActivityStopped
	if _, err := repository.SaveCAS(job, token); err != nil {
		t.Fatal(err)
	}
	rpc.status = aria2.LifecycleStatus{GID: job.ID, Status: "active", Dir: workDir}
	if err := application.RetryManaged(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if rpc.forceCalls != 1 || rpc.clearCalls != 1 {
		t.Fatalf("native detach calls: force=%d clear=%d", rpc.forceCalls, rpc.clearCalls)
	}
	if _, err := os.Stat(workDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging survived recovered Remove: %v", err)
	}
}

func TestStagedStorageRetryRestoresEmptyWorkTask(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	target := filepath.Join(root, "downloads")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	current := state.State{RuntimeSchemaVersion: 2, RPCPort: 6800, RPCSecret: "secret", SessionPath: servicePaths.SessionFile, StartupInputPath: servicePaths.StartupInputFile}
	if err := state.Save(servicePaths.StateFile, current); err != nil {
		t.Fatal(err)
	}
	rpc := &lifecycleRPC{addMakesVisible: true}
	application := New(Options{Paths: servicePaths, RPC: rpc, DownloadDir: target})
	result, err := application.AddManaged(context.Background(), AddRequest{Source: "https://example.test/payload.bin", TargetDir: target})
	if err != nil {
		t.Fatal(err)
	}
	repository := jobs.New(servicePaths.StateDir)
	job, token, err := repository.Load(result.Task.GID)
	if err != nil {
		t.Fatal(err)
	}
	job.ProblemCode = "StorageOffline"
	scope, err := repository.LoadStorage(job.StorageID)
	if err != nil {
		t.Fatal(err)
	}
	scope.Capability = jobs.PublicationCapability{}
	if err := repository.SaveStorage(scope); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SaveCAS(job, token); err != nil {
		t.Fatal(err)
	}
	rpc.status, rpc.statusErr = aria2.LifecycleStatus{}, &aria2.RPCError{Method: "aria2.tellStatus", Code: 1, Message: "not found"}
	if err := application.RetryManaged(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	job, _, err = repository.Load(job.ID)
	if err != nil || job.ProblemCode != "" || rpc.status.GID != job.ID {
		t.Fatalf("staged Retry did not converge: job=%+v status=%+v err=%v", job, rpc.status, err)
	}
	scope, err = repository.LoadStorage(job.StorageID)
	if err != nil || !scope.Capability.NoReplace {
		t.Fatalf("Retry did not refresh publication capability: %+v err=%v", scope.Capability, err)
	}
}

func TestPublishingRetryDetachesLiveResultBeforeReconcile(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	target := filepath.Join(root, "downloads")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := state.Save(servicePaths.StateFile, state.State{RuntimeSchemaVersion: 2}); err != nil {
		t.Fatal(err)
	}
	rpc := &lifecycleRPC{clearMakesAbsent: true}
	application := New(Options{Paths: servicePaths, RPC: rpc})
	result, err := application.AddManaged(context.Background(), AddRequest{Source: "https://example.test/payload.bin", TargetDir: target})
	if err != nil {
		t.Fatal(err)
	}
	repository := jobs.New(servicePaths.StateDir)
	job, token, err := repository.Load(result.Task.GID)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := repository.LoadStorage(job.StorageID)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(jobs.WorkDir(scope, job.ID), "payload.bin")
	if err := os.WriteFile(source, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := publication.Identify(source)
	if err != nil {
		t.Fatal(err)
	}
	job.Phase, job.PayloadRoot, job.PayloadIdentity = jobs.PhasePublishing, "payload.bin", jobIdentity(identity)
	if _, err := repository.SaveCAS(job, token); err != nil {
		t.Fatal(err)
	}
	rpc.status = aria2.LifecycleStatus{GID: job.ID, Status: "complete", Dir: jobs.WorkDir(scope, job.ID)}
	if err := application.RetryManaged(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if rpc.clearCalls != 1 {
		t.Fatalf("live publication result was not detached: clear=%d", rpc.clearCalls)
	}
	if _, err := os.Stat(filepath.Join(target, "payload.bin")); err != nil {
		t.Fatalf("publication did not reconcile: %v", err)
	}
}

func TestPublishedSeedStartRequiresExistingPublishedPayload(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	target := filepath.Join(root, "downloads")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := state.Save(servicePaths.StateFile, state.State{RuntimeSchemaVersion: 2}); err != nil {
		t.Fatal(err)
	}
	rpc := &lifecycleRPC{statusErr: &aria2.RPCError{Method: "aria2.tellStatus", Code: 1, Message: "not found"}}
	application := New(Options{Paths: servicePaths, RPC: rpc})
	result, err := application.AddManaged(context.Background(), AddRequest{Source: "magnet:?xt=urn:btih:0123456789012345678901234567890123456789", TargetDir: target})
	if err != nil {
		t.Fatal(err)
	}
	repository := jobs.New(servicePaths.StateDir)
	job, token, err := repository.Load(result.Task.GID)
	if err != nil {
		t.Fatal(err)
	}
	job.Phase = jobs.PhasePublished
	job.ActivityIntent = jobs.ActivityStopped
	job.PayloadRoot = "missing.bin"
	job.PayloadIdentity = jobs.ObjectIdentity{MountID: job.TargetIdentity.MountID, ObjectID: 42, ReliableAcrossRename: true}
	if _, err := repository.SaveCAS(job, token); err != nil {
		t.Fatal(err)
	}
	if err := application.SetActivity(context.Background(), job.ID, true); err == nil {
		t.Fatal("missing published payload was accepted for final seeding")
	}
	if rpc.torrentCalls != 0 {
		t.Fatalf("AddTorrent ran before payload validation: %d", rpc.torrentCalls)
	}
}

func TestRemovedPublishedCleanupMustConvergeBeforeClear(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	target := filepath.Join(root, "downloads")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := state.Save(servicePaths.StateFile, state.State{RuntimeSchemaVersion: 2}); err != nil {
		t.Fatal(err)
	}
	rpc := &lifecycleRPC{statusErr: &aria2.RPCError{Method: "aria2.tellStatus", Code: 1, Message: "not found"}}
	application := New(Options{Paths: servicePaths, RPC: rpc})
	result, err := application.AddManaged(context.Background(), AddRequest{Source: "https://example.test/payload.bin", TargetDir: target})
	if err != nil {
		t.Fatal(err)
	}
	repository := jobs.New(servicePaths.StateDir)
	job, token, err := repository.Load(result.Task.GID)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := repository.LoadStorage(job.StorageID)
	if err != nil {
		t.Fatal(err)
	}
	workDir := jobs.WorkDir(scope, job.ID)
	payload := filepath.Join(workDir, "payload.bin")
	if err := os.WriteFile(payload, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := publication.Identify(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publication.MoveNoReplace(payload, filepath.Join(target, "payload.bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "payload.bin.aria2"), []byte("control"), 0o600); err != nil {
		t.Fatal(err)
	}
	job.Phase, job.ActivityIntent = jobs.PhaseRemoved, jobs.ActivityStopped
	job.PayloadRoot, job.PayloadIdentity, job.ProblemCode = "payload.bin", jobIdentity(identity), "CleanupFailed"
	if _, err := repository.SaveCAS(job, token); err != nil {
		t.Fatal(err)
	}
	if err := application.ClearManaged(context.Background(), job.ID); err == nil {
		t.Fatal("Clear accepted residual published staging artifacts")
	}
	if err := application.RetryManaged(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published staging residue survived Retry: %v", err)
	}
	if err := application.ClearManaged(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPublishingActionsMatchLifecycleMutations(t *testing.T) {
	session := &DashboardSession{}
	job := jobs.Job{Phase: jobs.PhasePublishing}
	if actions := session.availableActions(TaskClassification{Status: StatusDownloading}, true, job); len(actions) != 0 {
		t.Fatalf("healthy Publishing exposed impossible actions: %v", actions)
	}
	job.ProblemCode = "PublicationStateUncertain"
	if actions := session.availableActions(TaskClassification{Status: StatusError}, true, job); len(actions) != 1 || actions[0] != "retry" {
		t.Fatalf("uncertain Publishing actions = %v", actions)
	}
}

func TestWeakIdentityPublicationAbandonRequiresDestinationOnlyState(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	target := filepath.Join(root, "downloads")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	current := state.State{RuntimeSchemaVersion: 2, RPCPort: 6800, RPCSecret: "secret", SessionPath: servicePaths.SessionFile, StartupInputPath: servicePaths.StartupInputFile}
	if err := state.Save(servicePaths.StateFile, current); err != nil {
		t.Fatal(err)
	}
	rpc := &lifecycleRPC{statusErr: &aria2.RPCError{Method: "aria2.tellStatus", Code: 1, Message: "not found"}}
	application := New(Options{Paths: servicePaths, RPC: rpc, DownloadDir: target})
	result, err := application.AddManaged(context.Background(), AddRequest{Source: "https://example.test/payload.bin", TargetDir: target})
	if err != nil {
		t.Fatal(err)
	}
	repository := jobs.New(servicePaths.StateDir)
	job, token, err := repository.Load(result.Task.GID)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := repository.LoadStorage(job.StorageID)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(jobs.WorkDir(scope, job.ID), "payload.bin")
	if err := os.WriteFile(source, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := publication.Identify(source)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(target, "payload.bin")
	if _, err := publication.MoveNoReplace(source, destination); err != nil {
		t.Fatal(err)
	}
	job.Phase, job.PayloadRoot, job.ProblemCode = jobs.PhasePublishing, "payload.bin", "PublicationRecoveryRequired"
	job.PayloadIdentity = jobIdentity(identity)
	job.PayloadIdentity.ReliableAcrossRename = false
	if _, err := repository.SaveCAS(job, token); err != nil {
		t.Fatal(err)
	}
	if err := application.ClearManaged(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "payload" {
		t.Fatalf("metadata abandon touched final payload: %q err=%v", data, err)
	}
}

func TestStableEmptyLegacyProofRejectsContentAndSymlink(t *testing.T) {
	root := t.TempDir()
	empty := filepath.Join(root, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := stableEmptyFile(empty); err != nil || !ok {
		t.Fatalf("empty proof ok=%v err=%v", ok, err)
	}
	nonempty := filepath.Join(root, "nonempty")
	if err := os.WriteFile(nonempty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, _ := stableEmptyFile(nonempty); ok {
		t.Fatal("non-empty legacy session accepted")
	}
	symlink := filepath.Join(root, "link")
	if err := os.Symlink(empty, symlink); err != nil {
		t.Fatal(err)
	}
	if ok, _ := stableEmptyFile(symlink); ok {
		t.Fatal("symlink legacy session accepted")
	}
}

type gateService struct {
	loaded, running bool
	uninstallCalls  int
	stopCalls       int
}

func (*gateService) Install(context.Context) error { return nil }
func (service *gateService) Uninstall(context.Context) error {
	service.uninstallCalls++
	service.loaded, service.running = false, false
	return nil
}
func (*gateService) Start(context.Context) error { return nil }
func (service *gateService) Stop(context.Context) error {
	service.stopCalls++
	service.running = false
	return nil
}
func (service *gateService) IsLoaded(context.Context) bool {
	return service.loaded
}
func (service *gateService) IsRunning(context.Context) bool {
	return service.running
}

func TestLegacyInstallGateRefusesRunningOrNonemptyStateAndRequiresExplicitDiscard(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	backend := &gateService{loaded: true, running: true}
	application := New(Options{Paths: servicePaths, Service: backend})
	if err := application.legacyInstallGate(context.Background(), false); err == nil || backend.uninstallCalls != 0 {
		t.Fatalf("running legacy service was mutated: err=%v calls=%d", err, backend.uninstallCalls)
	}
	backend.running = false
	if err := os.MkdirAll(filepath.Dir(servicePaths.LegacySessionFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(servicePaths.LegacySessionFile, []byte("legacy task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := application.legacyInstallGate(context.Background(), false); err == nil || backend.uninstallCalls != 0 {
		t.Fatalf("nonempty legacy session was accepted or supervisor mutated: err=%v calls=%d", err, backend.uninstallCalls)
	}
	if err := application.legacyInstallGate(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if backend.uninstallCalls != 1 {
		t.Fatalf("discard did not disable legacy supervisor: %d", backend.uninstallCalls)
	}
	data, err := os.ReadFile(servicePaths.LegacySessionFile)
	if err != nil || string(data) != "legacy task\n" {
		t.Fatalf("legacy session was modified: %q err=%v", data, err)
	}
}

func TestLegacyInstallGateDiscardStopsRunningSupervisor(t *testing.T) {
	for _, test := range []struct {
		name               string
		loaded             bool
		wantStopCalls      int
		wantUninstallCalls int
	}{
		{name: "loaded", loaded: true, wantUninstallCalls: 1},
		{name: "disabled but active", wantStopCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			servicePaths := paths.NewDarwin(filepath.Join(t.TempDir(), "home"))
			backend := &gateService{loaded: test.loaded, running: true}
			application := New(Options{Paths: servicePaths, Service: backend})

			if err := application.legacyInstallGate(context.Background(), true); err != nil {
				t.Fatalf("discard running legacy service: %v", err)
			}
			if backend.loaded || backend.running {
				t.Fatalf("legacy service still active: loaded=%v running=%v", backend.loaded, backend.running)
			}
			if backend.stopCalls != test.wantStopCalls || backend.uninstallCalls != test.wantUninstallCalls {
				t.Fatalf("stop calls=%d uninstall calls=%d", backend.stopCalls, backend.uninstallCalls)
			}
		})
	}
}

func TestRuntimeUpgradeRefusesPreexistingNonemptyV2Session(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	if err := os.MkdirAll(filepath.Dir(servicePaths.SessionFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(servicePaths.SessionFile, []byte("unexpected task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	application := New(Options{Paths: servicePaths, Service: &gateService{}, RenderService: func(state.State) (string, error) { return "service", nil }})
	desired := state.State{RuntimeSchemaVersion: 2, SessionPath: servicePaths.SessionFile, LogPath: servicePaths.LogFile}
	if _, err := application.reconcileManagedRuntime(context.Background(), desired); err == nil {
		t.Fatal("nonempty pre-commit v2 session was reused")
	}
}

func TestCompleteCensusBlocksUnmanagedActiveTask(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	repository := jobs.New(servicePaths.StateDir)
	managed := jobs.Job{ID: "0123456789abcdef", Source: "https://example.test/file", TargetDir: filepath.Join(root, "target"), TargetIdentity: jobs.ObjectIdentity{MountID: 1, ObjectID: 1}, StorageID: "fedcba9876543210", Phase: jobs.PhaseStaged, ActivityIntent: jobs.ActivityRunning}
	if _, err := repository.Create(managed); err != nil {
		t.Fatal(err)
	}
	rpc := &lifecycleRPC{census: []aria2.LifecycleStatus{{GID: managed.ID, Status: "active", TotalLength: 10}, {GID: "unmanaged", Status: "active", TotalLength: 10}}}
	application := New(Options{Paths: servicePaths, RPC: rpc})
	current := state.State{RuntimeSchemaVersion: 2}
	if err := application.guardUnmanagedTasks(context.Background(), current, false); err == nil {
		t.Fatal("unmanaged active task did not block lifecycle stop")
	}
	rpc.census = rpc.census[:1]
	if err := application.guardUnmanagedTasks(context.Background(), current, false); err != nil {
		t.Fatalf("managed-only census blocked stop: %v", err)
	}
}
