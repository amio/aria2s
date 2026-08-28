// Package tui renders app-owned canonical task state and dispatches only the
// actions advertised for each row. It does not infer lifecycle ownership or
// publication state from native aria2 buckets.
package tui

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/amio/aria2s/internal/app"
	"github.com/amio/aria2s/internal/aria2"
)

/** DashboardService is the consumer-owned boundary executed only by typed commands. */
type DashboardService interface {
	StartupStatus() string
	Snapshot(context.Context, app.DashboardQuery) (app.DashboardRead, error)
	TaskDetail(context.Context, string) (app.TaskDetail, error)
	AddURI(context.Context, string, aria2.AddOptions) (app.AddResult, error)
	RecentDirs(context.Context) ([]string, error)
	DeleteRecentDir(context.Context, string) error
	DefaultDir() string
	Pause(context.Context, string) error
	Resume(context.Context, string) error
	Retry(context.Context, string) (app.RetryResult, error)
	Remove(context.Context, string) error
}

type Mode string

const (
	ModeList   Mode = "list"
	ModeAdd    Mode = "add"
	ModeDetail Mode = "detail"
)

type ListState struct {
	Requested     app.DashboardListWindow
	Applied       app.DashboardListWindow
	Snapshot      app.TaskSnapshot
	HasSnapshot   bool
	Attempted     bool
	LastSuccessAt time.Time
	LastError     error
}

type DetailState struct {
	RequestedGID   string
	AppliedGID     string
	Detail         app.TaskDetail
	HasDetail      bool
	SourceResolved bool
	LoadingVisible bool
	LoadingToken   uint64
	LastError      error
	SourceError    error
}

type cachedTaskDetail struct {
	Detail         app.TaskDetail
	SourceResolved bool
	UpdatedAt      time.Time
}

const detailCacheFreshFor = 10 * time.Second

type RefreshState struct {
	InFlight   bool
	Queued     bool
	Generation uint64
	TimerToken uint64
}

type actionKind string

const (
	actionPause  actionKind = "pause"
	actionResume actionKind = "resume"
	actionReseed actionKind = "reseed"
	actionRetry  actionKind = "retry"
	actionRemove actionKind = "remove"
)

type Model struct {
	ctx             context.Context
	service         DashboardService
	refreshInterval time.Duration
	mode            Mode
	list            ListState
	detailState     DetailState
	detailCache     map[string]cachedTaskDetail
	refreshState    RefreshState
	pending         map[string]actionKind
	actionErrors    map[string]error
	addPending      bool
	addError        error
	openPending     bool
	desiredGID      string
	lastUnknownAdd  *addIntent
	snapshot        app.TaskSnapshot
	selected        int
	width           int
	height          int
	stoppedPage     int
	stoppedLimit    int
	addForm         AddForm
	detail          app.TaskDetail
	detailScroll    int
	loaded          bool
	loadingFrame    int
	startupMessage  string
	version         string
	notice          string
	noticeID        uint64
}

type snapshotResultMsg struct {
	generation uint64
	query      app.DashboardQuery
	read       app.DashboardRead
	err        error
}
type refreshTimerMsg struct{ token uint64 }
type startupStatusMsg struct{ message string }
type detailLoadingMsg struct {
	gid   string
	token uint64
}
type loadingTickMsg struct{}
type recentDirsMsg struct {
	dirs []string
	err  error
}
type recentDirDeleteResultMsg struct {
	dir string
	err error
}
type actionResultMsg struct {
	kind        actionKind
	gid         string
	replacement string
	warning     error
	err         error
}
type addIntent struct {
	uri       string
	options   aria2.AddOptions
	clipboard bool
}
type addResultMsg struct {
	result app.AddResult
	err    error
	intent addIntent
}
type clipboardContentMsg struct {
	uri string
	err error
}
type openResultMsg struct{ err error }
type noticeExpiredMsg struct{ id uint64 }

var runtimeGOOS = runtime.GOOS
var startExternalCommand = func(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

func NewModel(ctx context.Context, service DashboardService, refreshInterval time.Duration, version string) Model {
	if refreshInterval <= 0 {
		refreshInterval = time.Second
	}
	if version == "" {
		version = "dev"
	}
	return Model{ctx: ctx, service: service, refreshInterval: refreshInterval, mode: ModeList, stoppedLimit: 100, version: version, pending: make(map[string]actionKind), actionErrors: make(map[string]error), detailCache: make(map[string]cachedTaskDetail), refreshState: RefreshState{Generation: 1, InFlight: true}, list: ListState{Requested: app.DashboardListWindow{WaitingLimit: 100, StoppedLimit: 100}}}
}

func (model Model) Init() tea.Cmd {
	return tea.Batch(model.snapshotCmd(model.refreshState.Generation, model.query()), startupStatusCmd(model.service), loadingTick())
}

func (model Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case snapshotResultMsg:
		return model.applySnapshot(msg)
	case refreshTimerMsg:
		if msg.token != model.refreshState.TimerToken {
			return model, nil
		}
		return model.requestRefresh(false)
	case startupStatusMsg:
		if model.list.HasSnapshot {
			return model, nil
		}
		model.startupMessage = msg.message
		return model, startupStatusTick(model.service)
	case detailLoadingMsg:
		if msg.token != model.detailState.LoadingToken ||
			msg.gid != model.detailState.RequestedGID ||
			model.detailState.AppliedGID == msg.gid && model.detailState.HasDetail {
			return model, nil
		}
		model.detailState.LoadingVisible = true
	case loadingTickMsg:
		if model.loaded && model.startupMessage == "" {
			return model, nil
		}
		model.loadingFrame++
		return model, loadingTick()
	case recentDirsMsg:
		model.addForm = model.addForm.WithRecents(msg.dirs)
		if msg.err != nil {
			return model.setNotice(msg.err)
		}
		if model.mode == ModeAdd {
			return model, model.addForm.BlinkCmd()
		}
	case recentDirDeleteResultMsg:
		if msg.err != nil {
			return model.setNotice(msg.err)
		}
		model.addForm = model.addForm.WithoutRecentDir(msg.dir)
	case actionResultMsg:
		if pending, ok := model.pending[msg.gid]; !ok || pending != msg.kind {
			return model, nil
		}
		delete(model.pending, msg.gid)
		if msg.err == nil {
			if cached, ok := model.detailCache[msg.gid]; ok {
				cached.UpdatedAt = time.Time{}
				model.detailCache[msg.gid] = cached
			}
		}
		model.refreshState.Generation++
		if msg.replacement != "" {
			model.desiredGID = msg.replacement
			if model.mode == ModeDetail {
				model.detailState.RequestedGID = msg.replacement
				model.detailState.SourceResolved = false
				model.detailState.LastError = nil
				model.detailState.SourceError = nil
				model.detail = app.TaskDetail{}
			}
		}
		feedbackGID := msg.gid
		if msg.replacement != "" {
			feedbackGID = msg.replacement
		}
		if msg.err != nil {
			model.actionErrors[feedbackGID] = outcomeMessage(msg.err)
		} else if msg.warning != nil {
			model.actionErrors[feedbackGID] = msg.warning
		} else {
			delete(model.actionErrors, feedbackGID)
		}
		return model.requestRefresh(true)
	case addResultMsg:
		model.addPending = false
		model.refreshState.Generation++
		if msg.err != nil {
			if errors.Is(msg.err, aria2.ErrOutcomeUnknown) {
				intent := msg.intent
				model.lastUnknownAdd = &intent
			}
			model.addError = outcomeMessage(msg.err)
			return model.requestRefresh(true)
		}
		model.desiredGID = msg.result.GID
		model.lastUnknownAdd = nil
		model.addError = nil
		if msg.result.Warning != nil {
			model.addError = msg.result.Warning
		}
		if !msg.intent.clipboard {
			model.addForm = model.addForm.Reset()
			model.mode = ModeList
		}
		return model.requestRefresh(true)
	case clipboardContentMsg:
		return model.handleClipboardAdd(msg)
	case openResultMsg:
		model.openPending = false
		if msg.err != nil {
			return model.setNotice(msg.err)
		}
	case noticeExpiredMsg:
		if msg.id == model.noticeID {
			model.notice = ""
		}
	case cursorBlinkMsg:
		if model.mode == ModeAdd {
			model.addForm = model.addForm.Blink()
			return model, model.addForm.BlinkCmd()
		}
	case tea.WindowSizeMsg:
		model.width, model.height = msg.Width, msg.Height
	case tea.PasteMsg:
		return model.handlePaste(msg)
	case tea.KeyPressMsg:
		return model.handleKey(msg)
	}
	return model, nil
}

func (model Model) query() app.DashboardQuery {
	query := app.DashboardQuery{List: model.list.Requested, DetailGID: model.detailState.RequestedGID}
	if cached, ok := model.detailCache[query.DetailGID]; ok && cached.SourceResolved && time.Since(cached.UpdatedAt) < detailCacheFreshFor {
		query.DetailGID = ""
	}
	query.ResolveDetailSource = query.DetailGID != "" && (model.detailState.AppliedGID != query.DetailGID || !model.detailState.SourceResolved)
	return query
}

func (model Model) requestRefresh(immediate bool) (tea.Model, tea.Cmd) {
	if immediate {
		model.refreshState.TimerToken++
	}
	if model.refreshState.InFlight {
		model.refreshState.Queued = true
		return model, nil
	}
	model.refreshState.InFlight = true
	return model, model.snapshotCmd(model.refreshState.Generation, model.query())
}

func (model Model) snapshotCmd(generation uint64, query app.DashboardQuery) tea.Cmd {
	return func() tea.Msg {
		read, err := model.service.Snapshot(model.ctx, query)
		return snapshotResultMsg{generation: generation, query: query, read: read, err: err}
	}
}

func (model Model) applySnapshot(msg snapshotResultMsg) (tea.Model, tea.Cmd) {
	model.refreshState.InFlight = false
	model.retainSnapshotDetail(&msg)
	current := msg.generation == model.refreshState.Generation
	if current {
		model.loaded = true
		model.list.Attempted = true
		if msg.err != nil {
			model.list.LastError = msg.err
			model.startupMessage = ""
			if !model.list.HasSnapshot {
				model.startupMessage = startupMessage(msg.err)
			}
			if msg.query.DetailGID != "" {
				model.detailState.LastError = msg.err
				model.detailState.LoadingVisible = true
			}
		} else {
			model.startupMessage = ""
			if msg.read.ListErr != nil {
				model.list.LastError = msg.read.ListErr
			} else {
				selectedGID := model.Selected().GID
				model.list.Snapshot, model.snapshot = msg.read.Downloads, msg.read.Downloads
				model.list.Applied, model.list.HasSnapshot, model.list.LastError = msg.query.List, true, nil
				model.list.LastSuccessAt = time.Now()
				if model.desiredGID != "" {
					if _, found := indexOfGID(model.items(), model.desiredGID); found {
						selectedGID, model.desiredGID = model.desiredGID, ""
					}
				}
				model.selected = model.indexOf(selectedGID)
				model.refreshCachedDetailFromRow()
			}
			if msg.query.DetailGID != "" {
				model.detailState.LastError = msg.read.DetailErr
				if msg.read.Detail != nil {
					model.detailState.Detail, model.detail = *msg.read.Detail, *msg.read.Detail
					model.detailState.AppliedGID, model.detailState.HasDetail = msg.query.DetailGID, true
					model.detailState.LoadingVisible = false
					if cached, ok := model.detailCache[msg.query.DetailGID]; ok {
						model.detailState.SourceResolved = cached.SourceResolved
					}
				} else if msg.read.DetailErr != nil {
					model.detailState.LoadingVisible = true
				}
				// getUris is a one-shot fallback while resolving PrimaryURI. tellStatus may
				// already carry files/magnet; completed downloads often permanently answer
				// "No URI data is available", which must not retry every poll as SOURCE noise.
				model.detailState.SourceError = msg.read.DetailSourceErr
				if msg.read.Detail != nil && model.detailState.SourceResolved {
					model.detailState.SourceError = nil
				}
				// Transient getUris faults with empty PrimaryURI keep SourceError and retry.
			}
		}
	}
	if model.refreshState.Queued {
		model.refreshState.Queued = false
		return model.requestRefresh(true)
	}
	model.refreshState.TimerToken++
	token := model.refreshState.TimerToken
	return model, tea.Tick(model.refreshInterval, func(time.Time) tea.Msg { return refreshTimerMsg{token: token} })
}

func (model Model) Mode() Mode { return model.mode }
func (model Model) Selected() app.TaskRow {
	items := model.items()
	if model.selected < 0 || model.selected >= len(items) {
		return app.TaskRow{}
	}
	return items[model.selected]
}

func (model Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if isInputMode(model.mode) && isTextInputKey(msg) {
		return model.handleInputTextKey(msg)
	}
	switch model.mode {
	case ModeAdd:
		return model.handleAddKey(msg)
	case ModeDetail:
		return model.handleDetailKey(msg)
	default:
		return model.handleListKey(msg)
	}
}
func (model Model) handleInputTextKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if model.mode == ModeAdd {
		return model.applyAddForm(model.addForm.HandleKey(msg))
	}
	return model, nil
}
func (model Model) handlePaste(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	if model.mode == ModeAdd {
		return model.applyAddForm(model.addForm.HandlePaste(msg.Content))
	}
	return model, nil
}

func (model Model) handleListKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, dashboardKeys.List.Quit):
		return model, tea.Quit
	case key.Matches(msg, dashboardKeys.List.SelectDown):
		if model.selected < len(model.items())-1 {
			model.selected++
		}
	case key.Matches(msg, dashboardKeys.List.SelectUp):
		if model.selected > 0 {
			model.selected--
		}
	case key.Matches(msg, dashboardKeys.List.PasteURL):
		if !model.addPending {
			return model, readClipboardCommand(model.ctx)
		}
	case key.Matches(msg, dashboardKeys.List.Add):
		model.mode = ModeAdd
		model.addForm = NewAddForm(model.service.DefaultDir())
		return model, loadRecentDirs(model.ctx, model.service)
	case key.Matches(msg, dashboardKeys.List.Pause):
		if hasTaskAction(model.Selected(), "pause") {
			return model.startAction(actionPause)
		}
		return model.flashInapplicable("pause", model.Selected().CanonicalStatus)
	case key.Matches(msg, dashboardKeys.List.Resume):
		if hasTaskAction(model.Selected(), "retry") {
			return model.startAction(actionRetry)
		}
		if hasTaskAction(model.Selected(), "resume") {
			return model.startAction(actionResume)
		}
		if hasTaskAction(model.Selected(), "reseed") {
			return model.startAction(actionReseed)
		}
		return model.flashInapplicable("retry/resume", model.Selected().CanonicalStatus)
	case key.Matches(msg, dashboardKeys.List.Remove):
		if hasTaskAction(model.Selected(), "remove") {
			return model.startAction(actionRemove)
		}
		return model.flashInapplicable("remove", model.Selected().CanonicalStatus)
	case key.Matches(msg, dashboardKeys.List.NextPage):
		model.stoppedPage++
		model.list.Requested.StoppedOffset = model.stoppedPage * model.stoppedLimit
		model.refreshState.Generation++
		return model.requestRefresh(true)
	case key.Matches(msg, dashboardKeys.List.PrevPage):
		if model.stoppedPage > 0 {
			model.stoppedPage--
			model.list.Requested.StoppedOffset = model.stoppedPage * model.stoppedLimit
			model.refreshState.Generation++
			return model.requestRefresh(true)
		}
	case key.Matches(msg, dashboardKeys.List.Detail):
		return model.openDetailAt(model.selected)
	case key.Matches(msg, dashboardKeys.List.Open):
		return model.startOpen()
	}
	return model, nil
}

func hasTaskAction(download app.TaskRow, action string) bool {
	return hasAction(download.Actions, action)
}

func hasAction(actions []string, action string) bool {
	for _, candidate := range actions {
		if candidate == action {
			return true
		}
	}
	return false
}

func (model Model) handleAddKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	return model.applyAddForm(model.addForm.HandleKey(msg))
}
func (model Model) applyAddForm(form AddForm, cmd tea.Cmd, action AddFormAction) (tea.Model, tea.Cmd) {
	model.addForm = form
	switch action {
	case AddFormQuit:
		return model, tea.Quit
	case AddFormCancel:
		if !model.addPending {
			model.mode = ModeList
		}
		return model, nil
	case AddFormSubmit:
		if model.addPending {
			return model, nil
		}
		uri, dir := model.addForm.Values()
		if uri == "" {
			return model, nil
		}
		model.addPending = true
		return model, model.addCmd(uri, aria2.AddOptions{Dir: dir}, false)
	case AddFormDeleteRecent:
		dir, ok := model.addForm.SelectedRecentDir()
		if !ok {
			return model, nil
		}
		return model, deleteRecentDir(model.ctx, model.service, dir)
	default:
		return model, cmd
	}
}

func (model Model) addCmd(uri string, opts aria2.AddOptions, clipboard bool) tea.Cmd {
	intent := addIntent{uri: uri, options: opts, clipboard: clipboard}
	return func() tea.Msg {
		result, err := model.service.AddURI(model.ctx, uri, opts)
		return addResultMsg{result: result, err: err, intent: intent}
	}
}

func (model Model) handleDetailKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, dashboardKeys.Detail.Quit):
		return model, tea.Quit
	case key.Matches(msg, dashboardKeys.Detail.Back):
		model.mode = ModeList
		model.detailState.RequestedGID = ""
		model.refreshState.Generation++
		return model.requestRefresh(true)
	case key.Matches(msg, dashboardKeys.Detail.Next):
		return model.openDetailAt(model.selected + 1)
	case key.Matches(msg, dashboardKeys.Detail.Prev):
		return model.openDetailAt(model.selected - 1)
	case key.Matches(msg, dashboardKeys.Detail.ScrollDown):
		model.detailScroll++
	case key.Matches(msg, dashboardKeys.Detail.ScrollUp):
		if model.detailScroll > 0 {
			model.detailScroll--
		}
	case key.Matches(msg, dashboardKeys.Detail.NextPage):
		model.detailScroll += max(model.height/2, 5)
	case key.Matches(msg, dashboardKeys.Detail.PrevPage):
		model.detailScroll = max(0, model.detailScroll-max(model.height/2, 5))
	case key.Matches(msg, dashboardKeys.Detail.Open):
		return model.startOpen()
	case key.Matches(msg, dashboardKeys.Detail.Retry):
		if hasTaskAction(model.Selected(), "retry") {
			return model.startAction(actionRetry)
		}
		return model.flashInapplicable("retry", model.Selected().CanonicalStatus)
	}
	return model, nil
}

func (model Model) openDetailAt(index int) (tea.Model, tea.Cmd) {
	items := model.items()
	if index < 0 || index >= len(items) {
		return model, nil
	}
	model.selected, model.mode, model.detailScroll = index, ModeDetail, 0
	selected := items[index]
	gid := selected.GID
	if model.detailState.RequestedGID != gid {
		model.detailState.RequestedGID, model.detailState.SourceResolved, model.detailState.LastError, model.detailState.SourceError = gid, false, nil, nil
		model.detailState.LoadingVisible = false
		model.detailState.LoadingToken++
		model.refreshState.Generation++
		if cached, ok := model.detailCache[gid]; ok {
			model.detail = cached.Detail
			model.detailState.AppliedGID, model.detailState.HasDetail = gid, true
			model.detailState.Detail = model.detail
			model.detailState.SourceResolved = cached.SourceResolved
		} else if model.detailState.AppliedGID == gid && model.detailState.HasDetail {
			model.detail = model.detailState.Detail
		} else {
			model.detail = projectDownloadDetail(selected)
		}
		token := model.detailState.LoadingToken
		updated, refreshCmd := model.requestRefresh(true)
		model = updated.(Model)
		return model, tea.Batch(refreshCmd, detailLoadingTick(gid, token))
	}
	return model.requestRefresh(true)
}

// retainSnapshotDetail preserves independently valid detail reads even when
// navigation has already advanced the model generation. Only the current
// generation may apply list or detail state to the visible page.
func (model *Model) retainSnapshotDetail(msg *snapshotResultMsg) {
	if msg.err != nil || msg.query.DetailGID == "" || msg.read.Detail == nil {
		return
	}
	gid := msg.query.DetailGID
	detail := *msg.read.Detail
	previous, hadPrevious := model.detailCache[gid]
	entry := cachedTaskDetail{Detail: detail, UpdatedAt: time.Now()}
	if msg.generation != model.refreshState.Generation {
		entry.UpdatedAt = time.Time{}
	}
	if hadPrevious {
		entry.SourceResolved = previous.SourceResolved
		if detail.PrimaryURI == "" {
			entry.Detail.PrimaryURI = previous.Detail.PrimaryURI
		}
	}
	if entry.Detail.PrimaryURI != "" || msg.query.ResolveDetailSource && (msg.read.DetailSourceErr == nil || isAbsentURIData(msg.read.DetailSourceErr)) {
		entry.SourceResolved = true
	}
	model.detailCache[gid] = entry
	msg.read.Detail = &entry.Detail
}

func (model *Model) refreshCachedDetailFromRow() {
	row := model.Selected()
	cached, ok := model.detailCache[model.detailState.RequestedGID]
	if !ok || row.GID != model.detailState.RequestedGID {
		return
	}
	cached.Detail = mergeDetailRow(row, cached.Detail)
	model.detailCache[row.GID] = cached
	model.detailState.Detail, model.detail = cached.Detail, cached.Detail
}

func mergeDetailRow(row app.TaskRow, detail app.TaskDetail) app.TaskDetail {
	live := projectDownloadDetail(row)
	live.VerifiedLength, live.VerifyIntegrityPending = detail.VerifiedLength, detail.VerifyIntegrityPending
	live.PieceLength, live.NumPieces = detail.PieceLength, detail.NumPieces
	live.PrimaryURI, live.TargetDir = detail.PrimaryURI, detail.TargetDir
	live.ErrorCode, live.ErrorMessage, live.Files = detail.ErrorCode, detail.ErrorMessage, detail.Files
	return live
}

// projectDownloadDetail keeps detail navigation visually stable while the
// selected task's on-demand fields are still loading. AppliedGID and HasDetail
// continue to describe only authoritative RPC detail, so consumers that need
// file-level data still wait for or fetch the full payload.
func projectDownloadDetail(download app.TaskRow) app.TaskDetail {
	return app.TaskDetail{
		GID:             download.GID,
		Status:          download.Status,
		Name:            download.Name,
		IsMetadata:      download.IsMetadata,
		CompletedLength: download.CompletedLength,
		TotalLength:     download.TotalLength,
		LengthKnown:     download.LengthKnown,
		DownloadSpeed:   download.DownloadSpeed,
		UploadSpeed:     download.UploadSpeed,
		UploadLength:    download.UploadLength,
		InfoHash:        download.InfoHash,
		NumSeeders:      download.NumSeeders,
		Seeder:          download.Seeder,
		DownloadDir:     download.Dir,
		Connections:     download.Connections,
		CanonicalStatus: download.CanonicalStatus,
		Ownership:       download.Ownership,
		IssueCode:       download.IssueCode,
		IssueText:       download.IssueText,
		Actions:         download.Actions,
	}
}

func (model Model) startAction(kind actionKind) (tea.Model, tea.Cmd) {
	gid := model.Selected().GID
	if gid == "" {
		return model, nil
	}
	if _, exists := model.pending[gid]; exists {
		return model, nil
	}
	model.pending[gid] = kind
	return model, func() tea.Msg {
		var replacement string
		var warning, err error
		switch kind {
		case actionPause:
			err = model.service.Pause(model.ctx, gid)
		case actionResume:
			err = model.service.Resume(model.ctx, gid)
		case actionReseed:
			err = model.service.Resume(model.ctx, gid)
		case actionRemove:
			err = model.service.Remove(model.ctx, gid)
		case actionRetry:
			var result app.RetryResult
			result, err = model.service.Retry(model.ctx, gid)
			replacement, warning = result.NewGID, result.CleanupWarning
		}
		return actionResultMsg{kind: kind, gid: gid, replacement: replacement, warning: warning, err: err}
	}
}

func (model Model) startOpen() (tea.Model, tea.Cmd) {
	if model.openPending {
		return model, nil
	}
	gid := model.Selected().GID
	if gid == "" {
		return model, nil
	}
	model.openPending = true
	return model, func() tea.Msg {
		ctx, cancel := context.WithTimeout(model.ctx, localHelperTimeout)
		defer cancel()
		detail := model.detail
		var err error
		if model.detailState.AppliedGID != gid || !model.detailState.HasDetail {
			detail, err = model.service.TaskDetail(ctx, gid)
		}
		if err == nil {
			target := downloadTargetPath(detail)
			if target == "" {
				err = fmt.Errorf("download path is unavailable")
			} else {
				err = openInFileManager(ctx, target)
			}
		}
		return openResultMsg{err: err}
	}
}

func (model Model) handleClipboardAdd(msg clipboardContentMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return model.setNotice(msg.err)
	}
	if model.addPending {
		return model, nil
	}
	model.addPending = true
	dir := model.service.DefaultDir()
	return model, func() tea.Msg {
		dirs, err := model.service.RecentDirs(model.ctx)
		if err == nil && len(dirs) > 0 {
			dir = dirs[0]
		}
		if err != nil {
			return addResultMsg{err: err, intent: addIntent{uri: msg.uri, options: aria2.AddOptions{Dir: dir}, clipboard: true}}
		}
		intent := addIntent{uri: msg.uri, options: aria2.AddOptions{Dir: dir}, clipboard: true}
		result, err := model.service.AddURI(model.ctx, msg.uri, intent.options)
		return addResultMsg{result: result, err: err, intent: intent}
	}
}

func (model Model) items() []app.TaskRow {
	items := make([]app.TaskRow, 0, len(model.snapshot.Active)+len(model.snapshot.Waiting)+len(model.snapshot.Stopped))
	items = append(items, model.snapshot.Active...)
	items = append(items, model.snapshot.Waiting...)
	items = append(items, model.snapshot.Stopped...)
	// Stable ordering retains source order when the group comparator considers
	// two rows equivalent.
	sort.SliceStable(items, func(left, right int) bool {
		leftRank, rightRank := downloadStatusRank(items[left]), downloadStatusRank(items[right])
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		switch app.TaskStatus(items[left].CanonicalStatus) {
		case app.StatusMetadata:
			return newerAddedTask(items[left], items[right])
		case app.StatusSeeding, app.StatusComplete:
			return taskNameLess(items[left], items[right])
		case app.StatusDownloading:
			return lessCompleteTask(items[left], items[right])
		case app.StatusPaused:
			return moreCompleteTask(items[left], items[right])
		default:
			return false
		}
	})
	return items
}

var dashboardStatusOrder = []app.TaskStatus{
	app.StatusError,
	app.StatusPaused,
	app.StatusMetadata,
	app.StatusDownloading,
	app.StatusWaiting,
	app.StatusSeeding,
	app.StatusComplete,
}

func downloadStatusRank(download app.TaskRow) int {
	status := app.TaskStatus(download.CanonicalStatus)
	for rank, known := range dashboardStatusOrder {
		if status == known {
			return rank
		}
	}
	// Unknown states remain visible near other exceptional states.
	return len(dashboardStatusOrder) - 1
}

func newerAddedTask(left, right app.TaskRow) bool {
	if left.AddedAt.IsZero() != right.AddedAt.IsZero() {
		return !left.AddedAt.IsZero()
	}
	return left.AddedAt.After(right.AddedAt)
}

func taskNameLess(left, right app.TaskRow) bool {
	return strings.ToLower(left.Name) < strings.ToLower(right.Name)
}

func moreCompleteTask(left, right app.TaskRow) bool {
	leftKnown, rightKnown := left.TotalLength > 0, right.TotalLength > 0
	if leftKnown != rightKnown {
		return leftKnown
	}
	if !leftKnown {
		return false
	}
	leftProgress := float64(left.CompletedLength) / float64(left.TotalLength)
	rightProgress := float64(right.CompletedLength) / float64(right.TotalLength)
	return leftProgress > rightProgress
}

func lessCompleteTask(left, right app.TaskRow) bool {
	leftKnown, rightKnown := left.TotalLength > 0, right.TotalLength > 0
	if leftKnown != rightKnown {
		return leftKnown
	}
	if !leftKnown {
		return false
	}
	leftProgress := float64(left.CompletedLength) / float64(left.TotalLength)
	rightProgress := float64(right.CompletedLength) / float64(right.TotalLength)
	return leftProgress < rightProgress
}

func (model Model) indexOf(gid string) int {
	items := model.items()
	if len(items) == 0 {
		return 0
	}
	if index, found := indexOfGID(items, gid); found {
		return index
	}
	return min(model.selected, len(items)-1)
}

func indexOfGID(items []app.TaskRow, gid string) (int, bool) {
	for index, item := range items {
		if item.GID == gid {
			return index, true
		}
	}
	return 0, false
}

func pendingStatus(kind actionKind) string {
	switch kind {
	case actionPause:
		return "Pausing..."
	case actionResume:
		return "Resuming..."
	case actionReseed:
		return "Reseeding..."
	case actionRetry:
		return "Retrying..."
	case actionRemove:
		return "Removing..."
	default:
		return "Pending..."
	}
}

func (model Model) setNotice(err error) (tea.Model, tea.Cmd) {
	model.noticeID++
	model.notice = err.Error()
	id := model.noticeID
	return model, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return noticeExpiredMsg{id: id} })
}

/** isAbsentURIData reports aria2's permanent empty-URI answer (common for completed tasks). */
func isAbsentURIData(err error) bool {
	if err == nil {
		return false
	}
	var rpcErr *aria2.RPCError
	if errors.As(err, &rpcErr) {
		return strings.Contains(rpcErr.Message, "No URI data is available")
	}
	return strings.Contains(err.Error(), "No URI data is available")
}

/** flashInapplicable shows a short top-bar tip when a key is pressed on a wrong-status row. */
func (model Model) flashInapplicable(action, status string) (tea.Model, tea.Cmd) {
	if status == "" {
		return model, nil
	}
	return model.setNotice(errors.New(inapplicableActionMessage(action, status)))
}

func inapplicableActionMessage(action, status string) string {
	switch status {
	case "complete":
		return "already complete — " + action + " does nothing"
	case "active":
		return "already active — " + action + " does nothing"
	case "waiting":
		return "already waiting — " + action + " does nothing"
	case "paused":
		return "paused — " + action + " does nothing"
	case "error":
		return "failed task — " + action + " does nothing"
	case "removed":
		return "failed task — " + action + " does nothing"
	default:
		return "cannot " + action + " a " + status + " task"
	}
}
func outcomeMessage(err error) error {
	if errors.Is(err, aria2.ErrOutcomeUnknown) {
		return fmt.Errorf("outcome unknown; the action may have succeeded and will not be repeated: %w", err)
	}
	var userMessage interface{ UserMessage() string }
	if errors.As(err, &userMessage) {
		return errors.New(userMessage.UserMessage())
	}
	return err
}

func startupMessage(err error) string {
	var progress interface{ StartupMessage() string }
	if errors.As(err, &progress) {
		return progress.StartupMessage()
	}
	return ""
}

const loadingTickInterval = 80 * time.Millisecond
const startupStatusInterval = 250 * time.Millisecond
const detailLoadingDelay = 200 * time.Millisecond
const localHelperTimeout = 5 * time.Second

func loadingTick() tea.Cmd {
	return tea.Tick(loadingTickInterval, func(time.Time) tea.Msg { return loadingTickMsg{} })
}
func startupStatusCmd(service DashboardService) tea.Cmd {
	return func() tea.Msg { return startupStatusMsg{message: service.StartupStatus()} }
}
func startupStatusTick(service DashboardService) tea.Cmd {
	return tea.Tick(startupStatusInterval, func(time.Time) tea.Msg {
		return startupStatusMsg{message: service.StartupStatus()}
	})
}
func detailLoadingTick(gid string, token uint64) tea.Cmd {
	return tea.Tick(detailLoadingDelay, func(time.Time) tea.Msg {
		return detailLoadingMsg{gid: gid, token: token}
	})
}
func loadRecentDirs(ctx context.Context, service DashboardService) tea.Cmd {
	return func() tea.Msg { dirs, err := service.RecentDirs(ctx); return recentDirsMsg{dirs: dirs, err: err} }
}

func deleteRecentDir(ctx context.Context, service DashboardService, dir string) tea.Cmd {
	return func() tea.Msg {
		return recentDirDeleteResultMsg{dir: dir, err: service.DeleteRecentDir(ctx, dir)}
	}
}
func readClipboardCommand(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		helperCtx, cancel := context.WithTimeout(ctx, localHelperTimeout)
		defer cancel()
		content, err := readClipboardContent(helperCtx)
		if err != nil {
			return clipboardContentMsg{err: err}
		}
		uri := strings.TrimSpace(content)
		if uri == "" {
			return clipboardContentMsg{err: fmt.Errorf("clipboard is empty")}
		}
		if !isValidURI(uri) {
			return clipboardContentMsg{err: fmt.Errorf("not a valid URL or magnet link")}
		}
		return clipboardContentMsg{uri: uri}
	}
}
func readClipboardContent(ctx context.Context) (string, error) {
	var command *exec.Cmd
	switch runtimeGOOS {
	case "darwin":
		command = exec.CommandContext(ctx, "pbpaste")
	case "linux":
		command = exec.CommandContext(ctx, "xclip", "-selection", "clipboard", "-o")
	default:
		return "", fmt.Errorf("clipboard not supported on %s", runtimeGOOS)
	}
	data, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("read clipboard: %w", err)
	}
	return string(data), nil
}
func isValidURI(s string) bool {
	if strings.HasPrefix(s, "magnet:?") {
		return true
	}
	u, err := url.ParseRequestURI(s)
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "http", "https", "ftp", "ftps", "sftp":
		return true
	}
	return false
}
func openInFileManager(ctx context.Context, target string) error {
	info, err := os.Stat(target)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		dir := filepath.Dir(target)
		if dir == "" || dir == "." {
			return fmt.Errorf("download path is unavailable: %s", target)
		}
		return openInFileManagerPath(ctx, dir, true)
	}
	return openInFileManagerPath(ctx, target, info.IsDir())
}
func openInFileManagerPath(ctx context.Context, target string, isDir bool) error {
	switch runtimeGOOS {
	case "darwin":
		if isDir {
			return startExternalCommand(ctx, "open", target)
		}
		return startExternalCommand(ctx, "open", "-R", target)
	case "linux":
		dir := target
		if !isDir {
			dir = filepath.Dir(target)
		}
		if dir == "" || dir == "." {
			return fmt.Errorf("download path is unavailable: %s", target)
		}
		return startExternalCommand(ctx, "xdg-open", dir)
	default:
		return fmt.Errorf("opening downloads is unsupported on %s", runtimeGOOS)
	}
}
func downloadTargetPath(detail app.TaskDetail) string {
	if len(detail.Files) == 1 && detail.Files[0].Path != "" {
		return detail.Files[0].Path
	}
	if detail.DownloadDir != "" && detail.Name != "" {
		return filepath.Join(detail.DownloadDir, detail.Name)
	}
	return ""
}
