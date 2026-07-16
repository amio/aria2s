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
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/amio/aria2s/internal/app"
	"github.com/amio/aria2s/internal/aria2"
)

/** DashboardService is the consumer-owned boundary executed only by typed commands. */
type DashboardService interface {
	Snapshot(context.Context, aria2.DashboardQuery) (aria2.DashboardRead, error)
	TaskDetail(context.Context, string) (aria2.DownloadDetail, error)
	AddURI(context.Context, string, aria2.AddOptions) (app.AddResult, error)
	RecentDirs(context.Context) ([]string, error)
	DefaultDir() string
	Pause(context.Context, string) error
	Resume(context.Context, string) error
	Retry(context.Context, string) (app.RetryResult, error)
	Remove(context.Context, string) error
	ClearStopped(context.Context, string) error
}

type Mode string

const (
	ModeList   Mode = "list"
	ModeAdd    Mode = "add"
	ModeDetail Mode = "detail"
)

type ListState struct {
	Requested     aria2.ListQuery
	Applied       aria2.ListQuery
	Snapshot      aria2.DownloadSnapshot
	HasSnapshot   bool
	Attempted     bool
	LastSuccessAt time.Time
	LastError     error
}

type DetailState struct {
	RequestedGID   string
	AppliedGID     string
	Detail         aria2.DownloadDetail
	HasDetail      bool
	SourceResolved bool
	LastError      error
	SourceError    error
}

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
	actionRetry  actionKind = "retry"
	actionRemove actionKind = "remove"
	actionClear  actionKind = "clear"
)

type Model struct {
	ctx             context.Context
	service         DashboardService
	refreshInterval time.Duration
	mode            Mode
	list            ListState
	detailState     DetailState
	refreshState    RefreshState
	pending         map[string]actionKind
	actionErrors    map[string]error
	addPending      bool
	addError        error
	openPending     bool
	desiredGID      string
	lastUnknownAdd  *addIntent
	snapshot        aria2.DownloadSnapshot
	selected        int
	width           int
	height          int
	stoppedPage     int
	stoppedLimit    int
	addForm         AddForm
	detail          aria2.DownloadDetail
	detailScroll    int
	loaded          bool
	loadingFrame    int
	version         string
	notice          string
	noticeID        uint64
}

type snapshotResultMsg struct {
	generation uint64
	query      aria2.DashboardQuery
	read       aria2.DashboardRead
	err        error
}
type refreshTimerMsg struct{ token uint64 }
type loadingTickMsg struct{}
type recentDirsMsg struct {
	dirs []string
	err  error
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
	return Model{ctx: ctx, service: service, refreshInterval: refreshInterval, mode: ModeList, stoppedLimit: 100, version: version, pending: make(map[string]actionKind), actionErrors: make(map[string]error), refreshState: RefreshState{Generation: 1, InFlight: true}, list: ListState{Requested: aria2.ListQuery{WaitingLimit: 100, StoppedLimit: 100}}}
}

func (model Model) Init() tea.Cmd {
	return tea.Batch(model.snapshotCmd(model.refreshState.Generation, model.query()), loadingTick())
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
	case loadingTickMsg:
		if model.loaded {
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
	case actionResultMsg:
		if pending, ok := model.pending[msg.gid]; !ok || pending != msg.kind {
			return model, nil
		}
		delete(model.pending, msg.gid)
		model.refreshState.Generation++
		if msg.replacement != "" {
			model.desiredGID = msg.replacement
			if model.mode == ModeDetail {
				model.detailState.RequestedGID = msg.replacement
				model.detailState.SourceResolved = false
				model.detailState.LastError = nil
				model.detailState.SourceError = nil
				model.detail = aria2.DownloadDetail{}
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

func (model Model) query() aria2.DashboardQuery {
	query := aria2.DashboardQuery{List: model.list.Requested, DetailGID: model.detailState.RequestedGID}
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

func (model Model) snapshotCmd(generation uint64, query aria2.DashboardQuery) tea.Cmd {
	return func() tea.Msg {
		read, err := model.service.Snapshot(model.ctx, query)
		return snapshotResultMsg{generation: generation, query: query, read: read, err: err}
	}
}

func (model Model) applySnapshot(msg snapshotResultMsg) (tea.Model, tea.Cmd) {
	model.refreshState.InFlight = false
	current := msg.generation == model.refreshState.Generation
	if current {
		model.loaded = true
		model.list.Attempted = true
		if msg.err != nil {
			model.list.LastError = msg.err
			if msg.query.DetailGID != "" {
				model.detailState.LastError = msg.err
			}
		} else {
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
			}
			if msg.query.DetailGID != "" {
				model.detailState.LastError = msg.read.DetailErr
				if msg.read.Detail != nil {
					if msg.read.Detail.PrimaryURI == "" && msg.read.DetailSourceErr != nil && model.detailState.AppliedGID == msg.query.DetailGID {
						msg.read.Detail.PrimaryURI = model.detailState.Detail.PrimaryURI
					}
					model.detailState.Detail, model.detail = *msg.read.Detail, *msg.read.Detail
					model.detailState.AppliedGID, model.detailState.HasDetail = msg.query.DetailGID, true
				}
				model.detailState.SourceError = msg.read.DetailSourceErr
				if msg.read.Detail != nil && (msg.read.Detail.PrimaryURI != "" || msg.read.DetailSourceErr == nil) {
					model.detailState.SourceResolved = true
				}
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
func (model Model) Selected() aria2.Download {
	items := model.items()
	if model.selected < 0 || model.selected >= len(items) {
		return aria2.Download{}
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
		// forcePause only applies to live queue rows; stopped/complete GIDs reject it.
		switch model.Selected().Status {
		case "active", "waiting":
			return model.startAction(actionPause)
		}
		return model.flashInapplicable("pause", model.Selected().Status)
	case key.Matches(msg, dashboardKeys.List.Resume):
		// r is dual-purpose: unpause paused rows, re-queue failed ones. complete/removed
		// are not unpausable, and RetrySource intentionally rejects non-error statuses.
		switch model.Selected().Status {
		case "error":
			return model.startAction(actionRetry)
		case "paused":
			return model.startAction(actionResume)
		}
		return model.flashInapplicable("retry/resume", model.Selected().Status)
	case key.Matches(msg, dashboardKeys.List.Remove):
		if isStopped(model.Selected()) {
			return model.startAction(actionClear)
		}
		return model.startAction(actionRemove)
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
		if model.detail.Status == "error" {
			return model.startAction(actionRetry)
		}
		return model.flashInapplicable("retry", model.detail.Status)
	}
	return model, nil
}

func (model Model) openDetailAt(index int) (tea.Model, tea.Cmd) {
	items := model.items()
	if index < 0 || index >= len(items) {
		return model, nil
	}
	model.selected, model.mode, model.detailScroll = index, ModeDetail, 0
	gid := items[index].GID
	if model.detailState.RequestedGID != gid {
		model.detailState.RequestedGID, model.detailState.SourceResolved, model.detailState.LastError, model.detailState.SourceError = gid, false, nil, nil
		model.refreshState.Generation++
		if model.detailState.AppliedGID != gid {
			model.detail = aria2.DownloadDetail{}
		}
	}
	return model.requestRefresh(true)
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
		case actionRemove:
			err = model.service.Remove(model.ctx, gid)
		case actionClear:
			err = model.service.ClearStopped(model.ctx, gid)
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

func (model Model) items() []aria2.Download {
	items := make([]aria2.Download, 0, len(model.snapshot.Active)+len(model.snapshot.Waiting)+len(model.snapshot.Stopped))
	appendBucket := func(downloads []aria2.Download, want bool) {
		for _, d := range downloads {
			if d.IsMetadata == want {
				items = append(items, d)
			}
		}
	}
	appendBucket(model.snapshot.Active, false)
	appendBucket(model.snapshot.Active, true)
	appendBucket(model.snapshot.Waiting, true)
	appendBucket(model.snapshot.Stopped, true)
	appendBucket(model.snapshot.Waiting, false)
	appendBucket(model.snapshot.Stopped, false)
	return items
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

func indexOfGID(items []aria2.Download, gid string) (int, bool) {
	for index, item := range items {
		if item.GID == gid {
			return index, true
		}
	}
	return 0, false
}
func isStopped(download aria2.Download) bool {
	return download.Status == "complete" || download.Status == "error" || download.Status == "removed"
}

func pendingStatus(kind actionKind) string {
	switch kind {
	case actionPause:
		return "Pausing..."
	case actionResume:
		return "Resuming..."
	case actionRetry:
		return "Retrying..."
	case actionRemove:
		return "Removing..."
	case actionClear:
		return "Clearing..."
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
		return "task was removed — " + action + " does nothing"
	default:
		return "cannot " + action + " a " + status + " task"
	}
}
func outcomeMessage(err error) error {
	if errors.Is(err, aria2.ErrOutcomeUnknown) {
		return fmt.Errorf("outcome unknown; the action may have succeeded and will not be repeated: %w", err)
	}
	return err
}

const loadingTickInterval = 80 * time.Millisecond
const localHelperTimeout = 5 * time.Second

func loadingTick() tea.Cmd {
	return tea.Tick(loadingTickInterval, func(time.Time) tea.Msg { return loadingTickMsg{} })
}
func loadRecentDirs(ctx context.Context, service DashboardService) tea.Cmd {
	return func() tea.Msg { dirs, err := service.RecentDirs(ctx); return recentDirsMsg{dirs: dirs, err: err} }
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
func downloadTargetPath(detail aria2.DownloadDetail) string {
	if len(detail.Files) == 1 && detail.Files[0].Path != "" {
		return detail.Files[0].Path
	}
	if detail.DownloadDir != "" && detail.Name != "" {
		return filepath.Join(detail.DownloadDir, detail.Name)
	}
	return ""
}
