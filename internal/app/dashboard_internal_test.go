package app

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/paths"
	"github.com/amio/aria2s/internal/state"
)

type dashboardRPCStub struct {
	calls      []string
	identities []state.State
	source     aria2.RetrySource
	addGID     string
	addErr     error
	cleanupErr error
}

func (rpc *dashboardRPCStub) DashboardSnapshot(_ context.Context, current state.State, _ aria2.DashboardQuery) (aria2.DashboardRead, error) {
	rpc.identities = append(rpc.identities, current)
	return aria2.DashboardRead{}, nil
}
func (*dashboardRPCStub) TaskDetail(context.Context, state.State, string) (aria2.DownloadDetail, error) {
	return aria2.DownloadDetail{}, nil
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
