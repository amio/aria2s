package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/jobs"
	"github.com/amio/aria2s/internal/paths"
	"github.com/amio/aria2s/internal/state"
)

type dashboardRPCStub struct {
	calls       []string
	identities  []state.State
	snapshot    aria2.DashboardRead
	snapshotErr error
	source      aria2.RetrySource
	addGID      string
	addErr      error
	cleanupErr  error
	detail      aria2.DownloadDetail
	detailErr   error
	queries     []aria2.DashboardQuery
	detailGIDs  []string
}

func (rpc *dashboardRPCStub) DashboardSnapshot(_ context.Context, current state.State, query aria2.DashboardQuery) (aria2.DashboardRead, error) {
	rpc.identities = append(rpc.identities, current)
	rpc.queries = append(rpc.queries, query)
	return rpc.snapshot, rpc.snapshotErr
}
func (rpc *dashboardRPCStub) TaskDetail(_ context.Context, _ state.State, gid string) (aria2.DownloadDetail, error) {
	rpc.detailGIDs = append(rpc.detailGIDs, gid)
	return rpc.detail, rpc.detailErr
}

func TestDashboardMapsStableJobIDToReplaceableExecutionOnlyAtRPCBoundary(t *testing.T) {
	const jobID, executionGID = "1010101010101010", "2020202020202020"
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	if err := state.Save(servicePaths.StateFile, state.State{RPCSecret: "secret"}); err != nil {
		t.Fatal(err)
	}
	job := jobs.Job{ID: jobID, Source: "https://example.test/x", TargetDir: filepath.Join(root, "downloads"), TargetIdentity: jobs.ObjectIdentity{MountID: 1, ObjectID: 1}, StorageID: "3030303030303030", ActivityIntent: jobs.ActivityRunning, Payload: jobs.PayloadState{Location: jobs.PayloadStaging}, Execution: &jobs.ExecutionBinding{GID: executionGID}}
	if _, err := jobs.New(servicePaths.StateDir).Create(job); err != nil {
		t.Fatal(err)
	}
	rpc := &dashboardRPCStub{snapshot: aria2.DashboardRead{Downloads: aria2.DownloadSnapshot{Active: []aria2.Download{{GID: executionGID, Status: "active"}}}, Detail: &aria2.DownloadDetail{GID: executionGID, Status: "active"}}}
	session := &DashboardSession{app: New(Options{Paths: servicePaths, DashboardReadTimeout: time.Second}), rpc: rpc}
	read, err := session.Snapshot(context.Background(), aria2.DashboardQuery{DetailGID: jobID})
	if err != nil {
		t.Fatal(err)
	}
	if len(rpc.queries) != 1 || rpc.queries[0].DetailGID != executionGID || len(rpc.queries[0].ManagedGIDs) != 1 || rpc.queries[0].ManagedGIDs[0] != executionGID {
		t.Fatalf("RPC query did not use execution binding: %+v", rpc.queries)
	}
	if len(read.Downloads.Active) != 1 || read.Downloads.Active[0].GID != jobID || read.Detail == nil || read.Detail.GID != jobID {
		t.Fatalf("Dashboard exposed replaceable execution identity: %+v", read)
	}
}

func TestDashboardTaskDetailFallsBackToPublishedManifestAfterNativeDetach(t *testing.T) {
	const gid = "928cecc78f5f8415"
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	length := int64(1234)
	job := jobs.Job{
		ID:             gid,
		Source:         "https://example.test/payload.bin",
		TargetDir:      filepath.Join(root, "downloads"),
		TargetIdentity: jobs.ObjectIdentity{MountID: 1, ObjectID: 1},
		StorageID:      "928cecc78f5f8414",
		ActivityIntent: jobs.ActivityStopped,
		Payload: jobs.PayloadState{Location: jobs.PayloadPublished, Root: "payload.bin", FinalRoot: "payload (1).bin",
			Identity: jobs.ObjectIdentity{MountID: 1, ObjectID: 2}, Length: &length},
	}
	if _, err := jobs.New(servicePaths.StateDir).Create(job); err != nil {
		t.Fatal(err)
	}
	rpc := &dashboardRPCStub{detailErr: &aria2.RPCError{Method: "aria2.tellStatus", Code: 1, Message: "GID is not found"}}
	session := &DashboardSession{app: New(Options{Paths: servicePaths}), rpc: rpc}

	detail, err := session.TaskDetail(context.Background(), gid)
	if err != nil {
		t.Fatal(err)
	}
	if detail.DownloadDir != job.TargetDir || detail.Name != job.Payload.FinalRoot ||
		detail.CanonicalStatus != string(StatusComplete) || detail.CompletedLength != length ||
		detail.TotalLength != length || !detail.LengthKnown {
		t.Fatalf("manifest detail = %#v", detail)
	}
}

func TestDashboardSnapshotDoesNotQueryNativeDetailForManifestOnlyJob(t *testing.T) {
	const jobID = "9292929292929292"
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	job := jobs.Job{
		ID: jobID, Source: "https://example.test/x", TargetDir: filepath.Join(root, "downloads"),
		TargetIdentity: jobs.ObjectIdentity{MountID: 1, ObjectID: 1}, StorageID: "9393939393939393",
		ActivityIntent: jobs.ActivityRunning, Payload: jobs.PayloadState{Location: jobs.PayloadStaging},
	}
	if _, err := jobs.New(servicePaths.StateDir).Create(job); err != nil {
		t.Fatal(err)
	}
	rpc := &dashboardRPCStub{}
	session := &DashboardSession{app: New(Options{Paths: servicePaths, DashboardReadTimeout: time.Second}), rpc: rpc}
	read, err := session.Snapshot(context.Background(), aria2.DashboardQuery{DetailGID: jobID})
	if err != nil {
		t.Fatal(err)
	}
	if len(rpc.queries) != 1 || rpc.queries[0].DetailGID != "" {
		t.Fatalf("manifest-only JobID leaked to native detail query: %+v", rpc.queries)
	}
	if read.Detail == nil || read.Detail.GID != jobID {
		t.Fatalf("manifest detail was not projected: %+v", read.Detail)
	}
}
func (*dashboardRPCStub) AddURI(context.Context, state.State, string, aria2.AddOptions) (string, error) {
	return "added", nil
}
func (*dashboardRPCStub) Pause(context.Context, state.State, string) error  { return nil }
func (*dashboardRPCStub) Resume(context.Context, state.State, string) error { return nil }
func (rpc *dashboardRPCStub) RetrySource(context.Context, state.State, string) (aria2.RetrySource, error) {
	rpc.calls = append(rpc.calls, "source")
	return rpc.source, nil
}
func (rpc *dashboardRPCStub) AddURIs(context.Context, state.State, []string, aria2.AddOptions) (string, error) {
	rpc.calls = append(rpc.calls, "add")
	return rpc.addGID, rpc.addErr
}
func (rpc *dashboardRPCStub) Remove(context.Context, state.State, string) error {
	rpc.calls = append(rpc.calls, "remove")
	return nil
}
func (rpc *dashboardRPCStub) ClearStopped(context.Context, state.State, string) error {
	rpc.calls = append(rpc.calls, "cleanup")
	return rpc.cleanupErr
}

func TestDashboardDeleteRemovesUnmanagedMetadataAndItsResult(t *testing.T) {
	rpc := &dashboardRPCStub{}
	session := &DashboardSession{
		app: New(Options{
			Paths:                    paths.NewDarwin(t.TempDir()),
			DashboardMutationTimeout: time.Second,
		}),
		identity: state.State{RPCSecret: "bound"},
		rpc:      rpc,
	}

	if err := session.Delete(context.Background(), "metadata"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rpc.calls, []string{"remove", "cleanup"}) {
		t.Fatalf("metadata delete order = %v", rpc.calls)
	}
}

func TestDashboardRetryAddsBeforeCleanupAndPreservesConfirmedReplacement(t *testing.T) {
	rpc := &dashboardRPCStub{source: aria2.RetrySource{Status: "error", Dir: "/tmp", URIs: []string{"https://example.com/a"}}, addGID: "new", cleanupErr: errors.New("cleanup failed")}
	application := New(Options{DashboardMutationTimeout: time.Second})
	session := &DashboardSession{app: application, identity: state.State{RPCSecret: "bound"}, rpc: rpc}
	result, err := session.Retry(context.Background(), "old")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rpc.calls, []string{"source", "add", "cleanup"}) {
		t.Fatalf("wrong retry order: %v", rpc.calls)
	}
	if result.NewGID != "new" || result.CleanupWarning == nil {
		t.Fatalf("confirmed replacement lost: %#v", result)
	}
}

func TestDashboardRetryDoesNotCleanupAfterUnknownAdd(t *testing.T) {
	unknown := &aria2.OutcomeUnknownError{Method: "aria2.addUri", Cause: context.DeadlineExceeded}
	rpc := &dashboardRPCStub{source: aria2.RetrySource{Status: "error", URIs: []string{"https://example.com/a"}}, addErr: unknown}
	application := New(Options{DashboardMutationTimeout: time.Second})
	session := &DashboardSession{app: application, identity: state.State{}, rpc: rpc}
	if _, err := session.Retry(context.Background(), "old"); !errors.Is(err, aria2.ErrOutcomeUnknown) {
		t.Fatalf("unknown identity lost: %v", err)
	}
	if !reflect.DeepEqual(rpc.calls, []string{"source", "add"}) {
		t.Fatalf("old result was cleaned after unknown Add: %v", rpc.calls)
	}
}

func TestDashboardSessionBindsRPCIdentityButReadsFreshRecentDirs(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	initial := state.State{RPCPort: 6800, RPCSecret: "bound", RecentDirs: []string{"/old"}}
	if err := state.Save(servicePaths.StateFile, initial); err != nil {
		t.Fatal(err)
	}
	rpc := &dashboardRPCStub{}
	application := New(Options{Paths: servicePaths, DashboardReadTimeout: time.Second})
	session := &DashboardSession{app: application, identity: initial, rpc: rpc}
	updated := initial
	updated.RPCSecret = "replacement"
	updated.RecentDirs = []string{"/new"}
	if err := state.Save(servicePaths.StateFile, updated); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Snapshot(context.Background(), aria2.DashboardQuery{}); err != nil {
		t.Fatal(err)
	}
	dirs, err := session.RecentDirs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rpc.identities[0].RPCSecret != "bound" || !reflect.DeepEqual(dirs, []string{"/new"}) {
		t.Fatalf("identity/metadata ownership mismatch: identity=%q dirs=%v", rpc.identities[0].RPCSecret, dirs)
	}
}

func TestDashboardSnapshotReportsStartupHintWithoutHidingRPCCause(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	progress := startupProgress{phase: startupPhaseChecking, current: 3, total: 10}
	if err := writeStartupProgress(servicePaths.StartupProgressFile, progress); err != nil {
		t.Fatal(err)
	}
	rpcErr := errors.New("connection refused")
	rpc := &dashboardRPCStub{snapshotErr: rpcErr}
	session := &DashboardSession{app: New(Options{Paths: servicePaths, DashboardReadTimeout: time.Second}), rpc: rpc}

	_, err := session.Snapshot(context.Background(), aria2.DashboardQuery{})
	if !errors.Is(err, rpcErr) {
		t.Fatalf("snapshot error lost RPC cause: %v", err)
	}
	var hint interface{ StartupMessage() string }
	if !errors.As(err, &hint) || hint.StartupMessage() != "Checking task 3 of 10…" {
		t.Fatalf("startup hint = %T %v", err, err)
	}

	rpc.snapshotErr = nil
	if _, err := session.Snapshot(context.Background(), aria2.DashboardQuery{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(servicePaths.StartupProgressFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful snapshot left startup hint: %v", err)
	}
}

func TestDashboardSnapshotDecoratesDetailWhenListFails(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	detail := aria2.DownloadDetail{
		GID:        "a",
		Status:     "active",
		Name:       "example.iso",
		IsMetadata: true,
	}
	rpc := &dashboardRPCStub{snapshot: aria2.DashboardRead{
		ListErr: errors.New("list unavailable"),
		Detail:  &detail,
	}}
	application := New(Options{Paths: servicePaths, DashboardReadTimeout: time.Second})
	session := &DashboardSession{app: application, rpc: rpc}

	read, err := session.Snapshot(context.Background(), aria2.DashboardQuery{DetailGID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if read.ListErr == nil {
		t.Fatal("nested list failure was lost")
	}
	if read.Detail == nil || read.Detail.CanonicalStatus != string(StatusMetadata) ||
		read.Detail.Ownership != string(OwnershipUnmanaged) ||
		!reflect.DeepEqual(read.Detail.Actions, []string{"pause", "delete"}) {
		t.Fatalf("detail classification = %#v", read.Detail)
	}
}

func TestDashboardDecoratesManagedDetail(t *testing.T) {
	const jobID, executionGID = "928cecc78f5f8415", "928cecc78f5f8416"
	job := jobs.Job{
		ID:             jobID,
		TargetDir:      "/downloads",
		ActivityIntent: jobs.ActivityRunning,
		Payload:        jobs.PayloadState{Location: jobs.PayloadPublished},
		Execution:      &jobs.ExecutionBinding{GID: executionGID},
	}
	detail := aria2.DownloadDetail{
		GID:         executionGID,
		Status:      "active",
		DownloadDir: job.TargetDir,
	}
	session := &DashboardSession{}

	got := session.decorateSnapshot(
		aria2.DashboardRead{Detail: &detail},
		[]jobs.ScannedJob{{ID: jobID, Job: job}},
		aria2.DashboardQuery{DetailGID: executionGID},
	)

	if got.Detail == nil || got.Detail.CanonicalStatus != string(StatusDownloading) ||
		got.Detail.Ownership != string(OwnershipManaged) ||
		!reflect.DeepEqual(got.Detail.Actions, []string{"pause", "remove"}) {
		t.Fatalf("managed detail classification = %#v", got.Detail)
	}
}

func TestDashboardKeepsCompletedManagedMetadataInMetadataStateUntilPromotion(t *testing.T) {
	const jobID, executionGID = "928cecc78f5f8415", "928cecc78f5f8416"
	job := jobs.Job{
		ID:             jobID,
		TargetDir:      "/downloads",
		ActivityIntent: jobs.ActivityRunning,
		Payload:        jobs.PayloadState{Location: jobs.PayloadStaging},
		Execution:      &jobs.ExecutionBinding{GID: executionGID},
	}
	metadata := aria2.Download{
		GID:        executionGID,
		Status:     "complete",
		IsMetadata: true,
	}
	detail := aria2.DownloadDetail{
		GID:        executionGID,
		Status:     "complete",
		IsMetadata: true,
	}
	session := &DashboardSession{}

	got := session.decorateSnapshot(
		aria2.DashboardRead{
			Detail:  &detail,
			Managed: map[string]*aria2.Download{executionGID: &metadata},
		},
		[]jobs.ScannedJob{{ID: jobID, Job: job}},
		aria2.DashboardQuery{DetailGID: executionGID},
	)

	if len(got.Downloads.Stopped) != 1 || got.Downloads.Stopped[0].CanonicalStatus != string(StatusMetadata) ||
		!reflect.DeepEqual(got.Downloads.Stopped[0].Actions, []string{"pause", "delete"}) {
		t.Fatalf("managed metadata row classification = %#v", got.Downloads.Stopped)
	}
	if got.Detail == nil || got.Detail.CanonicalStatus != string(StatusMetadata) ||
		!reflect.DeepEqual(got.Detail.Actions, []string{"pause", "delete"}) {
		t.Fatalf("managed metadata detail classification = %#v", got.Detail)
	}
}

func TestDashboardProjectsDetachedPublishingAsRecoverableManifestDetail(t *testing.T) {
	const gid = "928cecc78f5f8415"
	job := jobs.Job{
		ID:             gid,
		Source:         "magnet:?xt=urn:btih:example",
		TargetDir:      "/downloads",
		ActivityIntent: jobs.ActivityRunning,
		Payload: jobs.PayloadState{Location: jobs.PayloadStaging, Root: "payload.cbr", FinalRoot: "payload.cbr",
			Identity: jobs.ObjectIdentity{MountID: 1, ObjectID: 2}},
	}
	session := &DashboardSession{}
	read := aria2.DashboardRead{
		Managed:         map[string]*aria2.Download{gid: nil},
		DetailErr:       &aria2.RPCError{Method: "aria2.tellStatus", Code: 1, Message: "GID is not found"},
		DetailSourceErr: &aria2.RPCError{Method: "aria2.getUris", Code: 1, Message: "GID is not found"},
	}
	got := session.decorateSnapshot(read, []jobs.ScannedJob{{ID: gid, Job: job}}, aria2.DashboardQuery{DetailGID: gid})
	if len(got.Downloads.Stopped) != 1 {
		t.Fatalf("manifest row count = %d", len(got.Downloads.Stopped))
	}
	row := got.Downloads.Stopped[0]
	if row.CanonicalStatus != string(StatusError) || row.IssueCode != "PublicationRecoveryRequired" || row.IssueText == "" || !reflect.DeepEqual(row.Actions, []string{"retry"}) {
		t.Fatalf("manifest recovery row = %#v", row)
	}
	if got.Detail == nil || got.Detail.GID != gid || got.Detail.CanonicalStatus != string(StatusError) ||
		got.Detail.IssueCode != "PublicationRecoveryRequired" ||
		got.Detail.PrimaryURI != job.Source || got.Detail.DownloadDir != job.TargetDir ||
		!reflect.DeepEqual(got.Detail.Actions, []string{"retry"}) ||
		got.DetailErr != nil || got.DetailSourceErr != nil {
		t.Fatalf("manifest recovery detail = %#v detailErr=%v sourceErr=%v", got.Detail, got.DetailErr, got.DetailSourceErr)
	}
}

func TestDashboardProjectsPublishedPayloadMetricsInRowAndDetail(t *testing.T) {
	const gid = "928cecc78f5f8415"
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	length := int64(1234)
	job := jobs.Job{
		ID:             gid,
		Source:         "https://example.test/payload.bin",
		TargetDir:      filepath.Join(root, "downloads"),
		ActivityIntent: jobs.ActivityStopped,
		Payload: jobs.PayloadState{Location: jobs.PayloadPublished, Root: "payload.bin", FinalRoot: "payload (1).bin",
			Identity: jobs.ObjectIdentity{MountID: 1, ObjectID: 2}, Length: &length},
	}
	session := &DashboardSession{app: New(Options{Paths: servicePaths})}
	notFound := &aria2.RPCError{Method: "aria2.tellStatus", Code: 1, Message: "GID is not found"}
	read := aria2.DashboardRead{
		Managed:   map[string]*aria2.Download{gid: nil},
		DetailErr: notFound,
	}

	got := session.decorateSnapshot(read, []jobs.ScannedJob{{ID: gid, Job: job}}, aria2.DashboardQuery{DetailGID: gid})
	if len(got.Downloads.Stopped) != 1 {
		t.Fatalf("manifest row count = %d", len(got.Downloads.Stopped))
	}
	row := got.Downloads.Stopped[0]
	if row.CanonicalStatus != string(StatusComplete) ||
		row.Name != job.Payload.FinalRoot ||
		row.CompletedLength != length || row.TotalLength != length || !row.LengthKnown {
		t.Fatalf("published row = %#v", row)
	}
	if got.Detail == nil || got.Detail.CanonicalStatus != string(StatusComplete) ||
		got.Detail.Name != job.Payload.FinalRoot ||
		got.Detail.CompletedLength != length || got.Detail.TotalLength != length ||
		!got.Detail.LengthKnown {
		t.Fatalf("published detail = %#v", got.Detail)
	}

	job.Payload.Length = nil
	unknown := session.decorateSnapshot(
		aria2.DashboardRead{Managed: map[string]*aria2.Download{gid: nil}},
		[]jobs.ScannedJob{{ID: gid, Job: job}},
		aria2.DashboardQuery{},
	)
	if len(unknown.Downloads.Stopped) != 1 ||
		unknown.Downloads.Stopped[0].CanonicalStatus != string(StatusComplete) ||
		unknown.Downloads.Stopped[0].LengthKnown {
		t.Fatalf("unknown legacy row = %#v", unknown.Downloads.Stopped)
	}
}

func TestDashboardKeepsNativeMetricsAuthoritativeForManagedTask(t *testing.T) {
	const jobID, executionGID = "928cecc78f5f8415", "928cecc78f5f8416"
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	manifestLength := int64(1234)
	createdAt := time.Date(2026, time.July, 20, 10, 0, 0, 0, time.UTC)
	job := jobs.Job{
		ID:             jobID,
		TargetDir:      filepath.Join(root, "downloads"),
		ActivityIntent: jobs.ActivityStopped,
		Payload: jobs.PayloadState{Location: jobs.PayloadPublished, Root: "payload.bin", FinalRoot: "payload.bin",
			Identity: jobs.ObjectIdentity{MountID: 1, ObjectID: 2}, Length: &manifestLength},
		Execution: &jobs.ExecutionBinding{GID: executionGID},
		CreatedAt: createdAt,
	}
	row := aria2.Download{
		GID:               executionGID,
		Status:            "complete",
		Dir:               job.TargetDir,
		CompletedLength:   99,
		TotalLength:       100,
		LengthKnown:       true,
		UploadLength:      77,
		UploadLengthKnown: true,
	}
	detail := aria2.DownloadDetail{
		GID:             executionGID,
		Status:          "complete",
		DownloadDir:     job.TargetDir,
		CompletedLength: 99,
		TotalLength:     100,
		LengthKnown:     true,
	}
	session := &DashboardSession{app: New(Options{Paths: servicePaths})}
	got := session.decorateSnapshot(
		aria2.DashboardRead{
			Downloads: aria2.DownloadSnapshot{Stopped: []aria2.Download{row}},
			Detail:    &detail,
			Managed:   map[string]*aria2.Download{executionGID: &row},
		},
		[]jobs.ScannedJob{{ID: jobID, Job: job}},
		aria2.DashboardQuery{DetailGID: executionGID},
	)

	if len(got.Downloads.Stopped) != 1 ||
		got.Downloads.Stopped[0].CompletedLength != 99 ||
		got.Downloads.Stopped[0].TotalLength != 100 ||
		got.Downloads.Stopped[0].UploadLength != 77 ||
		!got.Downloads.Stopped[0].UploadLengthKnown {
		t.Fatalf("native row metrics were overwritten: %#v", got.Downloads.Stopped)
	}
	if !got.Downloads.Stopped[0].AddedAt.Equal(createdAt) {
		t.Fatalf("managed added time = %v, want %v", got.Downloads.Stopped[0].AddedAt, createdAt)
	}
	if got.Detail == nil || got.Detail.CompletedLength != 99 || got.Detail.TotalLength != 100 {
		t.Fatalf("native detail metrics were overwritten: %#v", got.Detail)
	}
}
