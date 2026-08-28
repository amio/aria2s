package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amio/aria2s/internal/app"
	"github.com/amio/aria2s/internal/aria2"
	"github.com/charmbracelet/x/ansi"
)

type fakeService struct {
	reads              []app.DashboardRead
	queries            []app.DashboardQuery
	startupStatus      string
	addResult          app.AddResult
	addErr             error
	actions            []string
	retryResult        app.RetryResult
	retryErr           error
	recentDirs         []string
	deletedRecentDirs  []string
	deleteRecentDirErr error
	snapshotFunc       func(context.Context, app.DashboardQuery) (app.DashboardRead, error)
}

type presentableTestError struct{}

func (presentableTestError) Error() string       { return "internal diagnostic" }
func (presentableTestError) UserMessage() string { return "restore the files and retry" }

type startupTestError struct{ message string }

func (err startupTestError) Error() string          { return "connection refused" }
func (err startupTestError) StartupMessage() string { return err.message }

func keySpecial(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

func (service *fakeService) StartupStatus() string { return service.startupStatus }

func (service *fakeService) Snapshot(ctx context.Context, query app.DashboardQuery) (app.DashboardRead, error) {
	if service.snapshotFunc != nil {
		return service.snapshotFunc(ctx, query)
	}
	service.queries = append(service.queries, query)
	if len(service.reads) == 0 {
		return app.DashboardRead{}, nil
	}
	read := service.reads[0]
	service.reads = service.reads[1:]
	return read, nil
}
func (*fakeService) TaskDetail(context.Context, string) (app.TaskDetail, error) {
	return app.TaskDetail{}, nil
}
func (service *fakeService) AddURI(context.Context, string, aria2.AddOptions) (app.AddResult, error) {
	return service.addResult, service.addErr
}
func (service *fakeService) RecentDirs(context.Context) ([]string, error) {
	return service.recentDirs, nil
}
func (service *fakeService) DeleteRecentDir(_ context.Context, dir string) error {
	service.deletedRecentDirs = append(service.deletedRecentDirs, dir)
	return service.deleteRecentDirErr
}
func (*fakeService) DefaultDir() string { return "/tmp" }
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
	old := snapshotResultMsg{generation: 1, query: model.query(), read: app.DashboardRead{Downloads: app.TaskSnapshot{Active: []app.TaskRow{{GID: "old"}}}}}
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
	first := snapshotResultMsg{generation: 1, query: query, read: app.DashboardRead{Downloads: app.TaskSnapshot{Active: []app.TaskRow{{GID: "a"}}}}}
	updated, _ := model.Update(first)
	model = updated.(Model)
	model.refreshState.InFlight = true
	failed := snapshotResultMsg{generation: 1, query: query, read: app.DashboardRead{ListErr: errors.New("nested fault")}}
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
	detail := app.TaskDetail{GID: "a", Name: "task"}
	msg := snapshotResultMsg{generation: 1, query: query, read: app.DashboardRead{ListErr: errors.New("list"), Detail: &detail}}
	updated, _ := model.Update(msg)
	model = updated.(Model)
	if model.detailState.AppliedGID != "a" || model.detail.Name != "task" {
		t.Fatal("valid detail was discarded")
	}
}

func TestDetailSourceFailureRetainsPriorSourceForSameGID(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.detailState = DetailState{RequestedGID: "a", AppliedGID: "a", HasDetail: true, SourceResolved: false, Detail: app.TaskDetail{GID: "a", PrimaryURI: "magnet:?old"}}
	model.detail = model.detailState.Detail
	model.detailCache["a"] = cachedTaskDetail{Detail: model.detailState.Detail}
	detail := app.TaskDetail{GID: "a"}
	query := model.query()
	updated, _ := model.Update(snapshotResultMsg{generation: 1, query: query, read: app.DashboardRead{Downloads: app.TaskSnapshot{}, Detail: &detail, DetailSourceErr: errors.New("source timeout")}})
	model = updated.(Model)
	// Prior source is kept; known PrimaryURI means getUris fault is not user-facing SOURCE noise.
	if model.detail.PrimaryURI != "magnet:?old" || model.detailState.SourceError != nil || !model.detailState.SourceResolved {
		t.Fatalf("source partial merge failed: %#v", model.detailState)
	}
}

func TestDetailAbsentURIDataStopsSourceRetry(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.detailState = DetailState{RequestedGID: "a", SourceResolved: false}
	detail := app.TaskDetail{GID: "a"}
	query := model.query()
	if !query.ResolveDetailSource {
		t.Fatal("expected first detail open to resolve source")
	}
	sourceErr := &aria2.RPCError{Method: "aria2.getUris", Code: 1, Message: "No URI data is available for GID#a"}
	updated, _ := model.Update(snapshotResultMsg{generation: 1, query: query, read: app.DashboardRead{Downloads: app.TaskSnapshot{}, Detail: &detail, DetailSourceErr: sourceErr}})
	model = updated.(Model)
	if model.detailState.SourceError != nil || !model.detailState.SourceResolved {
		t.Fatalf("permanent no-URI answer should resolve silently: %#v", model.detailState)
	}
	if model.query().ResolveDetailSource {
		t.Fatal("subsequent polls must not re-call getUris")
	}
}

func TestDetailTransientSourceFaultRetries(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.detailState = DetailState{RequestedGID: "a", SourceResolved: false}
	detail := app.TaskDetail{GID: "a"}
	query := model.query()
	updated, _ := model.Update(snapshotResultMsg{generation: 1, query: query, read: app.DashboardRead{Downloads: app.TaskSnapshot{}, Detail: &detail, DetailSourceErr: errors.New("source timeout")}})
	model = updated.(Model)
	if model.detailState.SourceError == nil || model.detailState.SourceResolved {
		t.Fatalf("transient source fault should surface and retry: %#v", model.detailState)
	}
	if !model.query().ResolveDetailSource {
		t.Fatal("expected getUris retry while source remains unresolved")
	}
}

func TestUnknownMutationDoesNotRepeatAndQueuesReconciliation(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.snapshot.Active = []app.TaskRow{{GID: "a", Status: "active"}}
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

func TestOutcomeMessageUsesUserFacingIssueText(t *testing.T) {
	err := outcomeMessage(presentableTestError{})
	if got, want := err.Error(), "restore the files and retry"; got != want {
		t.Fatalf("outcome message = %q, want %q", got, want)
	}
}

func TestNavigationAndQuitRemainAvailableDuringRead(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.snapshot.Active = []app.TaskRow{{GID: "a"}, {GID: "b"}}
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

func TestAddCtrlDDeletesSelectedRecentAfterPersistence(t *testing.T) {
	service := &fakeService{}
	model := NewModel(context.Background(), service, time.Second, "dev")
	model.mode = ModeAdd
	model.addForm = NewAddForm("").WithRecents([]string{"/data/Movies", "/data/Music"})
	model.addForm.focus = focusDir

	updated, cmd := model.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("Ctrl+D did not request recent-dir deletion")
	}
	if len(model.addForm.recentDirs) != 2 {
		t.Fatal("recent dir was removed before persistence succeeded")
	}

	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if !reflect.DeepEqual(service.deletedRecentDirs, []string{"/data/Movies"}) {
		t.Fatalf("deleted recent dirs = %v", service.deletedRecentDirs)
	}
	if !reflect.DeepEqual(model.addForm.recentDirs, []string{"/data/Music"}) {
		t.Fatalf("form recent dirs = %v", model.addForm.recentDirs)
	}
}

func TestAddCtrlDKeepsRecentWhenPersistenceFails(t *testing.T) {
	service := &fakeService{deleteRecentDirErr: errors.New("save failed")}
	model := NewModel(context.Background(), service, time.Second, "dev")
	model.mode = ModeAdd
	model.addForm = NewAddForm("").WithRecents([]string{"/data/Movies"})
	model.addForm.focus = focusDir

	updated, cmd := model.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	model = updated.(Model)
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if !reflect.DeepEqual(model.addForm.recentDirs, []string{"/data/Movies"}) {
		t.Fatalf("failed persistence removed recent dir: %v", model.addForm.recentDirs)
	}
	if model.notice != "save failed" {
		t.Fatalf("failure notice = %q", model.notice)
	}
}

func TestDetailNavigationProjectsSelectedItemUntilDetailArrives(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.mode = ModeDetail
	model.loaded = true
	model.snapshot.Active = []app.TaskRow{
		{GID: "a", Name: "task-a", CanonicalStatus: "downloading"},
		{GID: "b", Name: "task-b", CanonicalStatus: "waiting", CompletedLength: 25, TotalLength: 100, LengthKnown: true},
	}
	model.detailState = DetailState{
		RequestedGID: "a",
		AppliedGID:   "a",
		Detail:       app.TaskDetail{GID: "a", Name: "task-a", CanonicalStatus: "downloading"},
		HasDetail:    true,
	}
	model.detail = model.detailState.Detail
	model.refreshState.InFlight = false

	updated, _ := model.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	model = updated.(Model)

	if model.detail.GID != "b" || model.detail.Name != "task-b" {
		t.Fatalf("selected row was not projected into detail: %#v", model.detail)
	}
	view := ansi.Strip(model.View().Content)
	if strings.Contains(view, "Loading details") || !strings.Contains(view, "task-b") {
		t.Fatalf("detail navigation rendered a loading shell instead of the selected item:\n%s", view)
	}
	if model.detailState.AppliedGID != "a" || !model.detailState.HasDetail {
		t.Fatalf("projection changed authoritative detail state: %#v", model.detailState)
	}

	updated, _ = model.Update(detailLoadingMsg{gid: "b", token: model.detailState.LoadingToken})
	model = updated.(Model)
	if !strings.Contains(ansi.Strip(model.View().Content), "Loading details") {
		t.Fatal("slow detail read did not show loading after the grace period")
	}
}

func TestDetailNavigationRestoresAppliedDetailDuringQueuedRead(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.mode = ModeDetail
	model.snapshot.Active = []app.TaskRow{{GID: "a", Name: "task-a"}, {GID: "b", Name: "task-b"}}
	model.detailState = DetailState{
		RequestedGID: "a",
		AppliedGID:   "a",
		Detail:       app.TaskDetail{GID: "a", Name: "full-task-a", PrimaryURI: "magnet:?a"},
		HasDetail:    true,
	}
	model.detail = model.detailState.Detail
	model.refreshState.InFlight = true

	updated, _ := model.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	model = updated.(Model)
	staleLoading := detailLoadingMsg{gid: "b", token: model.detailState.LoadingToken}
	updated, _ = model.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	model = updated.(Model)
	updated, _ = model.Update(staleLoading)
	model = updated.(Model)

	if model.detail.Name != "full-task-a" || model.detail.PrimaryURI != "magnet:?a" {
		t.Fatalf("applied detail was not restored when navigating back: %#v", model.detail)
	}
	if strings.Contains(ansi.Strip(model.View().Content), "Loading details") {
		t.Fatal("navigating back to applied detail rendered a loading shell")
	}
}

func TestDetailNavigationUsesCacheWhileRevalidating(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.mode = ModeDetail
	model.snapshot.Active = []app.TaskRow{
		{GID: "a", Name: "task-a"},
		{GID: "b", Name: "task-b", Status: "active", CanonicalStatus: "downloading", CompletedLength: 25, TotalLength: 100, LengthKnown: true, DownloadSpeed: 7, Actions: []string{"pause"}, InfoHash: "hash-b", Dir: "/downloads"},
	}
	model.detailCache["b"] = cachedTaskDetail{
		Detail: app.TaskDetail{
			GID: "b", Name: "task-b", Status: "paused", CanonicalStatus: "paused",
			CompletedLength: 10, TotalLength: 100, LengthKnown: true, PrimaryURI: "magnet:?b",
			InfoHash: "hash-b", DownloadDir: "/downloads", Files: []app.TaskFile{{Path: "/downloads/file", Length: 100, CompletedLength: 10}},
		},
		SourceResolved: true,
		UpdatedAt:      time.Now(),
	}
	model.refreshState.InFlight = false

	index, found := indexOfGID(model.items(), "b")
	if !found {
		t.Fatal("task b missing")
	}
	updated, cmd := model.openDetailAt(index)
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("cache hit did not request list refresh")
	}
	if model.detail.Name != "task-b" || model.detail.CompletedLength != 10 || model.detail.CanonicalStatus != "paused" || len(model.detail.Files) != 1 || model.detail.PrimaryURI != "magnet:?b" {
		t.Fatalf("cached detail was not restored immediately: %#v", model.detail)
	}
	query := model.query()
	if query.DetailGID != "" || query.ResolveDetailSource {
		t.Fatalf("fresh cache requested full detail: %+v", query)
	}
}

func TestExpiredDetailCacheRequestsFullRevalidation(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.detailState.RequestedGID = "a"
	model.detailState.AppliedGID = "a"
	model.detailState.SourceResolved = true
	model.detailCache["a"] = cachedTaskDetail{Detail: app.TaskDetail{GID: "a"}, SourceResolved: true, UpdatedAt: time.Now().Add(-detailCacheFreshFor)}
	if query := model.query(); query.DetailGID != "a" || query.ResolveDetailSource {
		t.Fatalf("expired cache query = %+v", query)
	}
}

func TestSuccessfulActionExpiresCachedDetail(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.pending["a"] = actionPause
	model.detailCache["a"] = cachedTaskDetail{Detail: app.TaskDetail{GID: "a"}, SourceResolved: true, UpdatedAt: time.Now()}
	updated, _ := model.Update(actionResultMsg{kind: actionPause, gid: "a"})
	if !updated.(Model).detailCache["a"].UpdatedAt.IsZero() {
		t.Fatal("successful action did not expire cached detail")
	}
}

func TestListRefreshUpdatesLiveFieldsWithoutDroppingCachedDetail(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.detailState = DetailState{RequestedGID: "a", AppliedGID: "a", HasDetail: true, SourceResolved: true}
	cached := cachedTaskDetail{Detail: app.TaskDetail{GID: "a", PrimaryURI: "magnet:?a", Files: []app.TaskFile{{Path: "/downloads/file"}}}, SourceResolved: true, UpdatedAt: time.Now()}
	model.detailCache["a"] = cached
	row := app.TaskRow{GID: "a", Name: "task-a", CompletedLength: 30, TotalLength: 100, LengthKnown: true, DownloadSpeed: 7, CanonicalStatus: "downloading", Actions: []string{"pause"}}

	updated, _ := model.Update(snapshotResultMsg{generation: 1, query: app.DashboardQuery{}, read: app.DashboardRead{Downloads: app.TaskSnapshot{Active: []app.TaskRow{row}}}})
	detail := updated.(Model).detail
	if detail.CompletedLength != 30 || detail.DownloadSpeed != 7 || len(detail.Files) != 1 || detail.PrimaryURI != "magnet:?a" || !reflect.DeepEqual(detail.Actions, []string{"pause"}) {
		t.Fatalf("merged detail = %#v", detail)
	}
}

func TestSupersededDetailResultPopulatesCacheWithoutApplyingPage(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.refreshState.Generation = 2
	model.detailState.RequestedGID = "b"
	detail := app.TaskDetail{GID: "a", Name: "task-a", Files: []app.TaskFile{{Path: "/downloads/a"}}}
	updated, _ := model.Update(snapshotResultMsg{
		generation: 1,
		query:      app.DashboardQuery{DetailGID: "a"},
		read:       app.DashboardRead{Detail: &detail},
	})
	model = updated.(Model)

	if _, ok := model.detailCache["a"]; !ok {
		t.Fatal("successful superseded detail was not cached")
	}
	if !model.detailCache["a"].UpdatedAt.IsZero() {
		t.Fatal("superseded detail should require revalidation before reuse")
	}
	if model.detail.GID == "a" || model.detailState.AppliedGID == "a" {
		t.Fatalf("superseded detail was applied to current page: detail=%#v state=%#v", model.detail, model.detailState)
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
	updated, _ := model.Update(snapshotResultMsg{generation: 1, query: query, read: app.DashboardRead{Downloads: app.TaskSnapshot{Active: []app.TaskRow{{GID: "old"}}}}})
	model = updated.(Model)
	if model.desiredGID != "new" {
		t.Fatal("desired selection was consumed before it appeared")
	}
	model.refreshState.InFlight = true
	updated, _ = model.Update(snapshotResultMsg{generation: 1, query: query, read: app.DashboardRead{Downloads: app.TaskSnapshot{Active: []app.TaskRow{{GID: "new"}}}}})
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

func TestDetailActionErrorRendersFullTextInBody(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.mode = ModeDetail
	model.loaded = true
	model.list.HasSnapshot = true
	model.snapshot.Active = []app.TaskRow{{GID: "a", Status: "active", Name: "task-a"}}
	model.detailState.RequestedGID = "a"
	model.detailState.AppliedGID = "a"
	model.detailState.HasDetail = true
	model.detail = app.TaskDetail{GID: "a", Name: "task-a", Status: "active"}
	full := "outcome unknown; the action may have succeeded and will not be repeated: aria2 mutation outcome unknown: context deadline exceeded"
	model.actionErrors["a"] = errors.New(full)
	view := ansi.Strip(model.View().Content)
	if !strings.Contains(view, "Action:") {
		t.Fatalf("detail body missing Action label:\n%s", view)
	}
	// Body wraps the full text across lines; assert head and tail both survive (not truncated to "...").
	head := "outcome unknown; the action may have succeeded and will not be repeated: aria2 mutation outcome"
	tail := "unknown: context deadline exceeded"
	if !strings.Contains(view, head) || !strings.Contains(view, tail) {
		t.Fatalf("detail body missing wrapped action error text (head=%q tail=%q):\n%s", head, tail, view)
	}
}

func TestIssueRendersWithDetailErrorsAndInSelectedListFeedback(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.loaded = true
	model.width = 180
	model.height = 40
	issue := "restore the seed files and retry"
	row := app.TaskRow{GID: "a", Name: "task-a", CanonicalStatus: "error", IssueCode: "FinalSeedPathMismatch", IssueText: issue}
	model.snapshot.Stopped = []app.TaskRow{row}
	model.actionErrors["a"] = errors.New(issue)

	listView := ansi.Strip(model.View().Content)
	if !strings.Contains(listView, "ISSUE: "+issue) {
		t.Fatalf("selected issue missing from list feedback:\n%s", listView)
	}
	if strings.Contains(listView, "ACTION: "+issue) || strings.Count(listView, issue) != 1 {
		t.Fatalf("durable issue duplicated by action feedback:\n%s", listView)
	}

	model.mode = ModeDetail
	model.detailState = DetailState{RequestedGID: "a", AppliedGID: "a", HasDetail: true}
	model.detail = app.TaskDetail{
		GID: "a", Name: "task-a", CanonicalStatus: "error", Ownership: "managed",
		IssueCode: "FinalSeedPathMismatch", IssueText: issue,
		ErrorCode: "13", ErrorMessage: "native disk failure",
	}
	detailView := ansi.Strip(model.View().Content)
	errorIndex := strings.Index(detailView, "Error 13:")
	issueIndex := strings.Index(detailView, "Issue:")
	downloadDirIndex := strings.Index(detailView, "Download Dir:")
	if errorIndex < 0 || issueIndex < 0 || downloadDirIndex < 0 || issueIndex < errorIndex || errorIndex < downloadDirIndex {
		t.Fatalf("issue is not grouped with detail errors:\n%s", detailView)
	}
	if strings.Contains(detailView, "Action:") || strings.Count(detailView, issue) != 1 {
		t.Fatalf("detail issue duplicated by action feedback:\n%s", detailView)
	}
}

func TestDetailShowsTargetAndOnlyDistinctTemporaryDirectory(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.loaded = true
	model.width = 180
	model.height = 40
	model.mode = ModeDetail
	model.detailState = DetailState{RequestedGID: "a", AppliedGID: "a", HasDetail: true}
	model.detail = app.TaskDetail{
		GID:         "a",
		Name:        "task-a",
		TargetDir:   "/downloads",
		DownloadDir: "/mnt/.aria2s_staging/storage/a",
	}

	downloadingView := ansi.Strip(model.View().Content)
	if !strings.Contains(downloadingView, "Download Dir:") || !strings.Contains(downloadingView, "/downloads") ||
		!strings.Contains(downloadingView, "Temporary Dir:") || !strings.Contains(downloadingView, "/mnt/.aria2s_staging/storage/a") {
		t.Fatalf("downloading detail directories =\n%s", downloadingView)
	}

	model.detail.DownloadDir = model.detail.TargetDir
	completedView := ansi.Strip(model.View().Content)
	if !strings.Contains(completedView, "Download Dir:") || !strings.Contains(completedView, "/downloads") || strings.Contains(completedView, "Temporary Dir:") {
		t.Fatalf("completed detail directories =\n%s", completedView)
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

func TestInitialStartupHintKeepsSpinnerAndSuppressesUnavailable(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	query := model.query()
	updated, _ := model.Update(snapshotResultMsg{
		generation: 1,
		query:      query,
		err:        startupTestError{message: "Checking task 3 of 10…"},
	})
	model = updated.(Model)
	view := ansi.Strip(model.View().Content)
	if !strings.Contains(view, "Checking task 3 of 10…") || strings.Contains(view, "UNAVAILABLE") || strings.Contains(view, "aria2 is unavailable") {
		t.Fatalf("startup view =\n%s", view)
	}
	frame := model.loadingFrame
	updated, cmd := model.Update(loadingTickMsg{})
	model = updated.(Model)
	if model.loadingFrame != frame+1 || cmd == nil {
		t.Fatal("startup hint did not keep the spinner active")
	}

	model.refreshState.InFlight = true
	updated, _ = model.Update(snapshotResultMsg{generation: 1, query: query})
	model = updated.(Model)
	if model.startupMessage != "" || !model.list.HasSnapshot {
		t.Fatalf("RPC success did not replace startup state: %#v", model.list)
	}
}

func TestStartupStatusUpdatesWhileInitialSnapshotIsInFlight(t *testing.T) {
	service := &fakeService{startupStatus: "Waiting for aria2 RPC…"}
	model := NewModel(context.Background(), service, time.Second, "dev")
	if !model.refreshState.InFlight {
		t.Fatal("test requires the initial snapshot to remain in flight")
	}

	message := startupStatusCmd(service)().(startupStatusMsg)
	updated, cmd := model.Update(message)
	model = updated.(Model)
	if model.startupMessage != service.startupStatus || cmd == nil {
		t.Fatalf("independent startup status was not applied: message=%q cmd=%v", model.startupMessage, cmd)
	}
	if !model.refreshState.InFlight {
		t.Fatal("startup status disturbed RPC single-flight state")
	}

	model.list.HasSnapshot = true
	updated, cmd = model.Update(startupStatusMsg{message: "stale"})
	model = updated.(Model)
	if cmd != nil || model.startupMessage == "stale" {
		t.Fatal("startup poll continued after the first snapshot")
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

func TestListResumeKeyDispatchesAdvertisedAction(t *testing.T) {
	cases := []struct {
		name      string
		native    string
		canonical string
		actions   []string
		want      string
	}{
		{name: "paused resumes", native: "paused", canonical: "paused", actions: []string{"resume"}, want: "resume:g1"},
		{name: "error retries", native: "error", canonical: "error", actions: []string{"retry"}, want: "retry:g1"},
		{name: "complete reseeds", native: "complete", canonical: "complete", actions: []string{"reseed"}, want: "resume:g1"},
		{name: "complete is no-op", native: "complete", canonical: "complete"},
		{name: "downloading without action is no-op", native: "active", canonical: "downloading"},
		{name: "waiting without action is no-op", native: "waiting", canonical: "waiting"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := &fakeService{}
			model := NewModel(context.Background(), service, time.Second, "dev")
			row := app.TaskRow{GID: "g1", Status: tc.native, CanonicalStatus: tc.canonical, Actions: tc.actions}
			model.snapshot.Stopped = []app.TaskRow{row}
			if tc.native == "active" {
				model.snapshot.Stopped = nil
				model.snapshot.Active = []app.TaskRow{row}
			}
			if tc.native == "waiting" {
				model.snapshot.Stopped = nil
				model.snapshot.Waiting = []app.TaskRow{row}
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
			if hasAction(tc.actions, "reseed") && pendingStatus(model.pending["g1"]) != "Reseeding..." {
				t.Fatalf("reseed pending status = %q", pendingStatus(model.pending["g1"]))
			}
			if _, ok := model.pending["g1"]; !ok {
				t.Fatal("pending action not recorded")
			}
		})
	}
}

func TestListRemoveKeyUsesXAndPermanentlyDeletesMetadata(t *testing.T) {
	service := &fakeService{}
	model := NewModel(context.Background(), service, time.Second, "dev")
	model.snapshot.Active = []app.TaskRow{{
		GID:             "metadata",
		Status:          "active",
		CanonicalStatus: "metadata",
		Actions:         []string{"pause", "remove"},
	}}

	updated, cmd := model.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	model = updated.(Model)
	if cmd != nil || len(service.actions) != 0 {
		t.Fatalf("legacy d key dispatched remove: cmd=%v actions=%v", cmd != nil, service.actions)
	}

	updated, cmd = model.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("metadata delete did not dispatch a command")
	}
	msg, ok := cmd().(actionResultMsg)
	if !ok || msg.kind != actionRemove || msg.err != nil {
		t.Fatalf("metadata delete result = %#v", msg)
	}
	if len(service.actions) != 1 || service.actions[0] != "remove:metadata" {
		t.Fatalf("metadata x action = %v", service.actions)
	}
	if pendingStatus(model.pending["metadata"]) != "Removing..." {
		t.Fatalf("metadata pending action = %q", pendingStatus(model.pending["metadata"]))
	}
}

func TestListPauseKeyOnlyTargetsLiveRows(t *testing.T) {
	service := &fakeService{}
	model := NewModel(context.Background(), service, time.Second, "dev")
	model.snapshot.Stopped = []app.TaskRow{{GID: "done", Status: "complete", CanonicalStatus: "complete"}}
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
	model.snapshot.Active = []app.TaskRow{{GID: "live", Status: "active", CanonicalStatus: "downloading", Actions: []string{"pause"}}}
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
	model.snapshot.Stopped = []app.TaskRow{{GID: "done", Status: "complete", CanonicalStatus: "complete", Name: "done.iso"}}
	updated, _ := model.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	model = updated.(Model)
	view := model.View().Content
	if !strings.Contains(view, "NOTE:") || !strings.Contains(view, "already complete") {
		t.Fatalf("list top bar missing inapplicable notice:\n%s", view)
	}
}

func TestParentCancellationStopsBlockedSnapshotCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	service := &fakeService{snapshotFunc: func(ctx context.Context, _ app.DashboardQuery) (app.DashboardRead, error) {
		<-ctx.Done()
		return app.DashboardRead{}, ctx.Err()
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
