package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/jobs"
	"github.com/amio/aria2s/internal/paths"
	"github.com/amio/aria2s/internal/state"
)

func TestInspectStartupFactRecognizesControlAndIgnoresPublishedStaging(t *testing.T) {
	root := t.TempDir()
	scope := jobs.StorageScope{ID: "fedcba9876543210", StagingAnchor: root}
	repository := jobs.New(filepath.Join(root, "state"))
	staged := jobs.Job{ID: "0123456789abcdef", StorageID: scope.ID, Payload: jobs.PayloadState{Location: jobs.PayloadStaging, Root: "payload"}}
	workDir := jobs.WorkDir(scope, staged.ID)
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"payload", "payload.aria2"} {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fact := inspectStartupFact(repository, staged, scope, true)
	if !fact.HasControl || fact.InferredRoot != staged.Payload.Root {
		t.Fatalf("staged fact = %+v", fact)
	}
	published := staged
	published.Payload.Location = jobs.PayloadPublished
	published.Payload.FinalRoot = published.Payload.Root
	fact = inspectStartupFact(repository, published, scope, true)
	if fact.InferredRoot != "" || fact.HasControl {
		t.Fatalf("published fact observed obsolete staging: %+v", fact)
	}
}

func TestManagedRuntimeArgsUseSafeFileAllocationOnlyWhenRequested(t *testing.T) {
	current := state.State{RPCPort: 6800, RPCSecret: "secret", StartupInputPath: "/state/startup", SessionPath: "/state/session"}
	normal := managedRuntimeArgs(current, "/state/hooks", false)
	if slices.Contains(normal, "--file-allocation=none") {
		t.Fatalf("normal startup overrides allocation: %v", normal)
	}
	safe := managedRuntimeArgs(current, "/state/hooks", true)
	if !slices.Contains(safe, "--file-allocation=none") || safe[len(safe)-1] != "--file-allocation=none" {
		t.Fatalf("safe startup allocation override = %v", safe)
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
func (service *gateService) IsLoaded(context.Context) bool  { return service.loaded }
func (service *gateService) IsRunning(context.Context) bool { return service.running }

func TestLegacyInstallGateStillRequiresExplicitDiscard(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	backend := &gateService{loaded: true, running: true}
	application := New(Options{Paths: servicePaths, Service: backend})
	err := application.legacyInstallGate(context.Background(), false)
	if err == nil || backend.uninstallCalls != 0 || !strings.Contains(err.Error(), "--version v0.4.0") {
		t.Fatalf("running legacy gate err=%v calls=%d", err, backend.uninstallCalls)
	}
	backend.running = false
	if err := os.MkdirAll(filepath.Dir(servicePaths.LegacySessionFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(servicePaths.LegacySessionFile, []byte("legacy task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := application.legacyInstallGate(context.Background(), false); err == nil {
		t.Fatal("nonempty legacy session was accepted")
	}
	if err := application.legacyInstallGate(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(servicePaths.LegacySessionFile)
	if err != nil || string(data) != "legacy task\n" {
		t.Fatalf("legacy data changed: %q err=%v", data, err)
	}
}

type censusRPC struct{ reconcilerRPC }

func (rpc *censusRPC) CompleteCensus(context.Context, state.State) ([]aria2.LifecycleStatus, error) {
	var result []aria2.LifecycleStatus
	for _, status := range rpc.statuses {
		result = append(result, status)
	}
	return result, nil
}

func TestCompleteCensusUsesExecutionBindingsForManagedOwnership(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	repository := jobs.New(servicePaths.StateDir)
	job := jobs.Job{ID: "0123456789abcdef", Source: "https://example.test/file", TargetDir: filepath.Join(root, "target"), TargetIdentity: jobs.ObjectIdentity{MountID: 1, ObjectID: 1}, StorageID: "fedcba9876543210", ActivityIntent: jobs.ActivityRunning, Payload: jobs.PayloadState{Location: jobs.PayloadStaging}, Execution: &jobs.ExecutionBinding{GID: "1111111111111111"}}
	if _, err := repository.Create(job); err != nil {
		t.Fatal(err)
	}
	rpc := &censusRPC{reconcilerRPC: reconcilerRPC{statuses: map[string]aria2.LifecycleStatus{job.Execution.GID: {GID: job.Execution.GID, Status: "active", TotalLength: 10}, "unmanaged": {GID: "unmanaged", Status: "active", TotalLength: 10}}}}
	application := New(Options{Paths: servicePaths, RPC: rpc})
	if err := application.guardUnmanagedTasks(context.Background(), state.State{RuntimeSchemaVersion: 2}, false); err == nil {
		t.Fatal("unmanaged active task did not block stop")
	}
	delete(rpc.statuses, "unmanaged")
	if err := application.guardUnmanagedTasks(context.Background(), state.State{RuntimeSchemaVersion: 2}, false); err != nil {
		t.Fatalf("managed execution was treated as unmanaged: %v", err)
	}
}
