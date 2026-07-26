package app

import (
	"context"
	"errors"
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
}

func (rpc *dashboardRPCStub) DashboardSnapshot(_ context.Context, current state.State, _ aria2.DashboardQuery) (aria2.DashboardRead, error) {
	rpc.identities = append(rpc.identities, current)
	return rpc.snapshot, rpc.snapshotErr
}
func (rpc *dashboardRPCStub) TaskDetail(context.Context, state.State, string) (aria2.DownloadDetail, error) {
	return rpc.detail, rpc.detailErr
}

func TestDashboardTaskDetailFallsBackToPublishedManifestAfterNativeDetach(t *testing.T) {
	const gid = "928cecc78f5f8415"
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	length := int64(1234)
	job := jobs.Job{
		ID:              gid,
		Source:          "https://example.test/payload.bin",
		TargetDir:       filepath.Join(root, "downloads"),
		TargetIdentity:  jobs.ObjectIdentity{MountID: 1, ObjectID: 1},
		StorageID:       "928cecc78f5f8414",
		Phase:           jobs.PhasePublished,
		ActivityIntent:  jobs.ActivityStopped,
		PayloadRoot:     "payload.bin",
		DestinationRoot: "payload (1).bin",
		PayloadIdentity: jobs.ObjectIdentity{MountID: 1, ObjectID: 2},
		PayloadLength:   &length,
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
	if detail.DownloadDir != job.TargetDir || detail.Name != job.DestinationRoot ||
		detail.CanonicalStatus != string(StatusComplete) || detail.CompletedLength != length ||
		detail.TotalLength != length || !detail.LengthKnown {
		t.Fatalf("manifest detail = %#v", detail)
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
func (*dashboardRPCStub) Remove(context.Context, state.State, string) error { return nil }
func (rpc *dashboardRPCStub) ClearStopped(context.Context, state.State, string) error {
	rpc.calls = append(rpc.calls, "cleanup")
	return rpc.cleanupErr
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
		!reflect.DeepEqual(read.Detail.Actions, []string{"pause", "remove"}) {
		t.Fatalf("detail classification = %#v", read.Detail)
	}
}

func TestDashboardDecoratesManagedDetail(t *testing.T) {
	const gid = "928cecc78f5f8415"
	job := jobs.Job{
		ID:             gid,
		TargetDir:      "/downloads",
		Phase:          jobs.PhasePublished,
		ActivityIntent: jobs.ActivityRunning,
	}
	detail := aria2.DownloadDetail{
		GID:         gid,
		Status:      "active",
		DownloadDir: job.TargetDir,
	}
	session := &DashboardSession{}

	got := session.decorateSnapshot(
		aria2.DashboardRead{Detail: &detail},
		[]jobs.ScannedJob{{ID: gid, Job: job}},
		aria2.DashboardQuery{DetailGID: gid},
	)

	if got.Detail == nil || got.Detail.CanonicalStatus != string(StatusDownloading) ||
		got.Detail.Ownership != string(OwnershipManaged) ||
		got.Detail.Phase != string(jobs.PhasePublished) ||
		!reflect.DeepEqual(got.Detail.Actions, []string{"pause", "remove"}) {
		t.Fatalf("managed detail classification = %#v", got.Detail)
	}
}

func TestDashboardProjectsDetachedPublishingAsRecoverableManifestDetail(t *testing.T) {
	const gid = "928cecc78f5f8415"
	job := jobs.Job{
		ID:             gid,
		Source:         "magnet:?xt=urn:btih:example",
		TargetDir:      "/downloads",
		Phase:          jobs.PhasePublishing,
		ActivityIntent: jobs.ActivityRunning,
		PayloadRoot:    "payload.cbr",
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
	if row.CanonicalStatus != string(StatusError) || row.ProblemCode != "PublicationRecoveryRequired" || !reflect.DeepEqual(row.Actions, []string{"retry"}) {
		t.Fatalf("manifest recovery row = %#v", row)
	}
	if got.Detail == nil || got.Detail.GID != gid || got.Detail.CanonicalStatus != string(StatusError) ||
		got.Detail.ProblemCode != "PublicationRecoveryRequired" ||
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
		ID:              gid,
		Source:          "https://example.test/payload.bin",
		TargetDir:       filepath.Join(root, "downloads"),
		Phase:           jobs.PhasePublished,
		ActivityIntent:  jobs.ActivityStopped,
		PayloadRoot:     "payload.bin",
		DestinationRoot: "payload (1).bin",
		PayloadLength:   &length,
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
		row.Name != job.DestinationRoot ||
		row.CompletedLength != length || row.TotalLength != length || !row.LengthKnown {
		t.Fatalf("published row = %#v", row)
	}
	if got.Detail == nil || got.Detail.CanonicalStatus != string(StatusComplete) ||
		got.Detail.Name != job.DestinationRoot ||
		got.Detail.CompletedLength != length || got.Detail.TotalLength != length ||
		!got.Detail.LengthKnown {
		t.Fatalf("published detail = %#v", got.Detail)
	}

	job.PayloadLength = nil
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
	const gid = "928cecc78f5f8415"
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	manifestLength := int64(1234)
	createdAt := time.Date(2026, time.July, 20, 10, 0, 0, 0, time.UTC)
	job := jobs.Job{
		ID:             gid,
		TargetDir:      filepath.Join(root, "downloads"),
		Phase:          jobs.PhasePublished,
		ActivityIntent: jobs.ActivityStopped,
		PayloadLength:  &manifestLength,
		CreatedAt:      createdAt,
	}
	row := aria2.Download{
		GID:               gid,
		Status:            "complete",
		Dir:               job.TargetDir,
		CompletedLength:   99,
		TotalLength:       100,
		LengthKnown:       true,
		UploadLength:      77,
		UploadLengthKnown: true,
	}
	detail := aria2.DownloadDetail{
		GID:             gid,
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
			Managed:   map[string]*aria2.Download{gid: &row},
		},
		[]jobs.ScannedJob{{ID: gid, Job: job}},
		aria2.DashboardQuery{DetailGID: gid},
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
