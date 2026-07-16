package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amio/aria2s/internal/app"
	"github.com/amio/aria2s/internal/aria2"
)

type fakeService struct {
	reads        []aria2.DashboardRead
	queries      []aria2.DashboardQuery
	addResult    app.AddResult
	addErr       error
	actions      []string
	retryResult  app.RetryResult
	retryErr     error
	snapshotFunc func(context.Context, aria2.DashboardQuery) (aria2.DashboardRead, error)
}

func keySpecial(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

func (service *fakeService) Snapshot(ctx context.Context, query aria2.DashboardQuery) (aria2.DashboardRead, error) {
	if service.snapshotFunc != nil {
		return service.snapshotFunc(ctx, query)
	}
	service.queries = append(service.queries, query)
	if len(service.reads) == 0 {
		return aria2.DashboardRead{}, nil
	}
	read := service.reads[0]
	service.reads = service.reads[1:]
	return read, nil
}
func (*fakeService) TaskDetail(context.Context, string) (aria2.DownloadDetail, error) {
	return aria2.DownloadDetail{}, nil
}
func (service *fakeService) AddURI(context.Context, string, aria2.AddOptions) (app.AddResult, error) {
	return service.addResult, service.addErr
}
func (*fakeService) RecentDirs(context.Context) ([]string, error) { return nil, nil }
func (*fakeService) DefaultDir() string                           { return "/tmp" }
func (service *fakeService) Pause(_ context.Context, gid string) error {
	service.actions = append(service.actions, "pause:"+gid)
	return nil
}
func (service *fakeService) Resume(_ context.Context, gid string) error {
	service.actions = append(service.actions, "resume:"+gid)
	return nil
}
func (service *fakeService) Retry(_ context.Context, gid string) (app.RetryResult, error) {
	service.actions = append(service.actions, "retry:"+gid)
	if service.retryResult.NewGID == "" {
		service.retryResult.NewGID = "new"
	}
	return service.retryResult, service.retryErr
}
func (service *fakeService) Remove(_ context.Context, gid string) error {
	service.actions = append(service.actions, "remove:"+gid)
	return nil
}
func (service *fakeService) ClearStopped(_ context.Context, gid string) error {
	service.actions = append(service.actions, "clear:"+gid)
	return nil
}

func TestRefreshCoordinatorCoalescesTriggersAndRejectsOldGeneration(t *testing.T) {
	service := &fakeService{}
	model := NewModel(context.Background(), service, time.Second, "dev")
	updated, cmd := model.Update(refreshTimerMsg{token: 0})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("trigger during initial read must coalesce")
	}
	if !model.refreshState.Queued {
		t.Fatal("expected queued refresh")
	}
	model.refreshState.Generation++
	old := snapshotResultMsg{generation: 1, query: model.query(), read: aria2.DashboardRead{Downloads: aria2.DownloadSnapshot{Active: []aria2.Download{{GID: "old"}}}}}
	updated, cmd = model.Update(old)
	model = updated.(Model)
	if model.list.HasSnapshot {
		t.Fatal("stale generation applied")
	}
	if cmd == nil {
		t.Fatal("queued refresh was not started")
	}
}

func TestPartialListFailurePreservesLastKnownGood(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	query := model.query()
	first := snapshotResultMsg{generation: 1, query: query, read: aria2.DashboardRead{Downloads: aria2.DownloadSnapshot{Active: []aria2.Download{{GID: "a"}}}}}
	updated, _ := model.Update(first)
	model = updated.(Model)
	model.refreshState.InFlight = true
	failed := snapshotResultMsg{generation: 1, query: query, read: aria2.DashboardRead{ListErr: errors.New("nested fault")}}
	updated, _ = model.Update(failed)
	model = updated.(Model)
	if !model.list.HasSnapshot || model.Selected().GID != "a" {
		t.Fatal("last known good snapshot was discarded")
	}
	if model.list.LastError == nil {
		t.Fatal("list error not recorded")
	}
}

func TestDetailResultCanApplyWhenListFails(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.detailState.RequestedGID = "a"
	query := model.query()
	detail := aria2.DownloadDetail{GID: "a", Name: "task"}
	msg := snapshotResultMsg{generation: 1, query: query, read: aria2.DashboardRead{ListErr: errors.New("list"), Detail: &detail}}
	updated, _ := model.Update(msg)
	model = updated.(Model)
	if model.detailState.AppliedGID != "a" || model.detail.Name != "task" {
		t.Fatal("valid detail was discarded")
	}
}

func TestDetailSourceFailureRetainsPriorSourceForSameGID(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.detailState = DetailState{RequestedGID: "a", AppliedGID: "a", HasDetail: true, SourceResolved: false, Detail: aria2.DownloadDetail{GID: "a", PrimaryURI: "magnet:?old"}}
	model.detail = model.detailState.Detail
	detail := aria2.DownloadDetail{GID: "a"}
	query := model.query()
	updated, _ := model.Update(snapshotResultMsg{generation: 1, query: query, read: aria2.DashboardRead{Downloads: aria2.DownloadSnapshot{}, Detail: &detail, DetailSourceErr: errors.New("source timeout")}})
	model = updated.(Model)
	if model.detail.PrimaryURI != "magnet:?old" || model.detailState.SourceError == nil {
		t.Fatalf("source partial merge failed: %#v", model.detailState)
	}
}

func TestUnknownMutationDoesNotRepeatAndQueuesReconciliation(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.snapshot.Active = []aria2.Download{{GID: "a", Status: "active"}}
	model.refreshState.InFlight = false
	updated, cmd := model.startAction(actionPause)
	model = updated.(Model)
	msg := cmd()
	result := msg.(actionResultMsg)
	result.err = &aria2.OutcomeUnknownError{Method: "aria2.forcePause", Cause: context.DeadlineExceeded}
	updated, refresh := model.Update(result)
	model = updated.(Model)
	if _, ok := model.pending["a"]; ok {
		t.Fatal("pending action did not terminate")
	}
	if refresh == nil {
		t.Fatal("unknown outcome did not reconcile")
	}
	if !errors.Is(result.err, aria2.ErrOutcomeUnknown) {
		t.Fatal("unknown identity lost")
	}
}

func TestNavigationAndQuitRemainAvailableDuringRead(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.snapshot.Active = []aria2.Download{{GID: "a"}, {GID: "b"}}
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	model = updated.(Model)
	if model.Selected().GID != "b" {
		t.Fatal("navigation blocked by read")
	}
	_, cmd := model.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil {
		t.Fatal("quit blocked by read")
	}
}

func TestBackClearsDetailTargetAndQueuesLatestRead(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.mode = ModeDetail
	model.detailState.RequestedGID = "a"
	model.refreshState.InFlight = false
	updated, cmd := model.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	model = updated.(Model)
	if model.mode != ModeList || model.detailState.RequestedGID != "" {
		t.Fatalf("detail target survived Back: %#v", model.detailState)
	}
	if cmd == nil {
		t.Fatal("Back did not request a list-only refresh")
	}
}

func TestDesiredSelectionWaitsUntilReplacementAppears(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.desiredGID = "new"
	query := model.query()
	updated, _ := model.Update(snapshotResultMsg{generation: 1, query: query, read: aria2.DashboardRead{Downloads: aria2.DownloadSnapshot{Active: []aria2.Download{{GID: "old"}}}}})
	model = updated.(Model)
	if model.desiredGID != "new" {
		t.Fatal("desired selection was consumed before it appeared")
	}
	model.refreshState.InFlight = true
	updated, _ = model.Update(snapshotResultMsg{generation: 1, query: query, read: aria2.DashboardRead{Downloads: aria2.DownloadSnapshot{Active: []aria2.Download{{GID: "new"}}}}})
	model = updated.(Model)
	if model.desiredGID != "" || model.Selected().GID != "new" {
		t.Fatalf("replacement not selected: desired=%q selected=%q", model.desiredGID, model.Selected().GID)
	}
}

func TestRetryReplacementRetargetsOpenDetail(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.mode = ModeDetail
	model.pending["old"] = actionRetry
	model.detailState.RequestedGID = "old"
	updated, _ := model.Update(actionResultMsg{kind: actionRetry, gid: "old", replacement: "new", warning: errors.New("cleanup failed")})
	model = updated.(Model)
	if model.detailState.RequestedGID != "new" || model.detail.GID != "" {
		t.Fatalf("detail was not retargeted: %#v", model.detailState)
	}
	if model.actionErrors["new"] == nil {
		t.Fatal("cleanup warning was not scoped to replacement")
	}
}

func TestAddAndDetailErrorsRenderInTheirOwningViews(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.mode = ModeAdd
	model.addError = errors.New("duplicate risk")
	if view := model.View().Content; !strings.Contains(view, "duplicate risk") {
		t.Fatalf("Add error missing:\n%s", view)
	}
	model.mode = ModeDetail
	model.detailState.RequestedGID = "a"
	model.detailState.LastError = errors.New("timeout")
	if view := model.View().Content; !strings.Contains(view, "Details unavailable") || !strings.Contains(view, "timeout") {
		t.Fatalf("detail error shell missing:\n%s", view)
	}
}

func TestInitialFailureRendersUnavailablePlaceholder(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.loaded = true
	model.list.Attempted = true
	model.list.LastError = errors.New("offline")
	if view := model.View().Content; !strings.Contains(view, "aria2 is unavailable") {
		t.Fatalf("unavailable placeholder missing:\n%s", view)
	}
}

func TestOldNoticeExpiryCannotClearAddError(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	updated, _ := model.setNotice(errors.New("clipboard"))
	model = updated.(Model)
	model.addError = errors.New("add failed")
	updated, _ = model.Update(noticeExpiredMsg{id: model.noticeID})
	model = updated.(Model)
	if model.notice != "" || model.addError == nil {
		t.Fatal("notice expiry cleared independent Add error")
	}
}

func TestListResumeKeyDispatchesByStatus(t *testing.T) {
	cases := []struct {
		name   string
		status string
		want   string
	}{
		{name: "paused resumes", status: "paused", want: "resume:g1"},
		{name: "error retries", status: "error", want: "retry:g1"},
		{name: "complete is no-op", status: "complete", want: ""},
		{name: "active is no-op", status: "active", want: ""},
		{name: "waiting is no-op", status: "waiting", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := &fakeService{}
			model := NewModel(context.Background(), service, time.Second, "dev")
			model.snapshot.Stopped = []aria2.Download{{GID: "g1", Status: tc.status}}
			if tc.status == "active" {
				model.snapshot.Stopped = nil
				model.snapshot.Active = []aria2.Download{{GID: "g1", Status: tc.status}}
			}
			if tc.status == "waiting" {
				model.snapshot.Stopped = nil
				model.snapshot.Waiting = []aria2.Download{{GID: "g1", Status: tc.status}}
			}
			updated, cmd := model.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
			model = updated.(Model)
			if tc.want == "" {
				if len(service.actions) != 0 {
					t.Fatalf("unexpected actions: %v", service.actions)
				}
				if model.notice == "" {
					t.Fatal("expected top-bar notice for inapplicable r")
				}
				if cmd == nil {
					t.Fatal("expected notice expiry command")
				}
				return
			}
			if model.notice != "" {
				t.Fatalf("notice set on successful action: %q", model.notice)
			}
			if cmd == nil {
				t.Fatal("expected action command")
			}
			msg := cmd()
			if _, ok := msg.(actionResultMsg); !ok {
				t.Fatalf("unexpected msg: %T", msg)
			}
			if len(service.actions) != 1 || service.actions[0] != tc.want {
				t.Fatalf("actions got %v, want [%s]", service.actions, tc.want)
			}
			if _, ok := model.pending["g1"]; !ok {
				t.Fatal("pending action not recorded")
			}
		})
	}
}

func TestListPauseKeyOnlyTargetsLiveRows(t *testing.T) {
	service := &fakeService{}
	model := NewModel(context.Background(), service, time.Second, "dev")
	model.snapshot.Stopped = []aria2.Download{{GID: "done", Status: "complete"}}
	updated, cmd := model.Update(tea.KeyPressMsg{Code: 'p', Text: "p"})
	model = updated.(Model)
	if len(service.actions) != 0 {
		t.Fatalf("pause on complete must not dispatch: %v", service.actions)
	}
	if model.notice == "" || cmd == nil {
		t.Fatal("pause on complete should flash a notice")
	}
	if !strings.Contains(model.notice, "complete") {
		t.Fatalf("notice got %q", model.notice)
	}

	model.notice = ""
	model.snapshot.Stopped = nil
	model.snapshot.Active = []aria2.Download{{GID: "live", Status: "active"}}
	updated, cmd = model.Update(tea.KeyPressMsg{Code: 'p', Text: "p"})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("pause on active should dispatch")
	}
	_ = cmd()
	if len(service.actions) != 1 || service.actions[0] != "pause:live" {
		t.Fatalf("actions got %v", service.actions)
	}
}

func TestInapplicableResumeNoticeRendersInListTopBar(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.loaded = true
	model.snapshot.Stopped = []aria2.Download{{GID: "done", Status: "complete", Name: "done.iso"}}
	updated, _ := model.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	model = updated.(Model)
	view := model.View().Content
	if !strings.Contains(view, "NOTE:") || !strings.Contains(view, "already complete") {
		t.Fatalf("list top bar missing inapplicable notice:\n%s", view)
	}
}

func TestParentCancellationStopsBlockedSnapshotCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	service := &fakeService{snapshotFunc: func(ctx context.Context, _ aria2.DashboardQuery) (aria2.DashboardRead, error) {
		<-ctx.Done()
		return aria2.DashboardRead{}, ctx.Err()
	}}
	model := NewModel(ctx, service, time.Second, "dev")
	result := make(chan tea.Msg, 1)
	go func() { result <- model.snapshotCmd(1, model.query())() }()
	cancel()
	select {
	case msg := <-result:
		if !errors.Is(msg.(snapshotResultMsg).err, context.Canceled) {
			t.Fatalf("blocked command returned %v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked snapshot ignored parent cancellation")
	}
}
