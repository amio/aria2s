package tui

import (
	"context"
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

	"github.com/amio/aria2s/internal/aria2"
)

/** Service is the state-backed task API used by the interactive dashboard. */
type Service interface {
	ListDownloads(context.Context, aria2.ListOptions) (aria2.DownloadSnapshot, error)
	TaskDetail(context.Context, string) (aria2.DownloadDetail, error)
	AddURI(context.Context, string, aria2.AddOptions) (string, error)
	RecentDirs(context.Context) ([]string, error)
	DefaultDir() string
	Pause(context.Context, string) error
	Resume(context.Context, string) error
	Remove(context.Context, string) error
	ClearStopped(context.Context, string) error
	// Subscribe returns a channel of aria2 WebSocket notification events.
	// When WebSocket is unavailable the implementation returns nil.
	Subscribe(context.Context) <-chan aria2.Notification
}

/** Mode identifies the current dashboard interaction surface. */
type Mode string

const (
	ModeList   Mode = "list"
	ModeAdd    Mode = "add"
	ModeDetail Mode = "detail"
)

/** Model is the Bubble Tea state for the aria2 dashboard. */
type Model struct {
	service         Service
	refreshInterval time.Duration
	wsEvents        <-chan aria2.Notification
	mode            Mode
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
	err             error
	errorInfo       string
	errorInfoTime   time.Time
}

type refreshMsg struct{}

type wsEventMsg struct {
	notification aria2.Notification
}

type wsDisconnectedMsg struct{}

type loadingTickMsg struct{}

type recentDirsMsg struct {
	dirs []string
	err  error
}

type actionResultMsg struct {
	err error
}

type clipboardContentMsg struct {
	uri string
	err error
}

var runtimeGOOS = runtime.GOOS

var startExternalCommand = func(name string, args ...string) error {
	return exec.Command(name, args...).Start()
}

func NewModel(service Service, refreshInterval time.Duration, version string) Model {
	if refreshInterval <= 0 {
		refreshInterval = time.Second
	}
	if version == "" {
		version = "dev"
	}
	return Model{
		service:         service,
		refreshInterval: refreshInterval,
		wsEvents:        service.Subscribe(context.Background()),
		mode:            ModeList,
		stoppedLimit:    100,
		version:         version,
	}
}

func (model Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		func() tea.Msg { return refreshMsg{} },
		loadingTick(),
	}
	if model.wsEvents != nil {
		cmds = append(cmds, waitForWSEvent(model.wsEvents))
	}
	return tea.Batch(cmds...)
}

func (model Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case refreshMsg:
		return model.refresh(), tick(model.refreshInterval)
	case wsEventMsg:
		return model.refresh(), waitForWSEvent(model.wsEvents)
	case wsDisconnectedMsg:
		model.wsEvents = nil
		return model, nil
	case loadingTickMsg:
		if model.loaded {
			return model, nil
		}
		model.loadingFrame++
		return model, loadingTick()
	case cursorBlinkMsg:
		if model.mode != ModeAdd {
			return model, nil
		}
		model.addForm = model.addForm.Blink()
		return model, model.addForm.BlinkCmd()
	case recentDirsMsg:
		model.addForm = model.addForm.WithRecents(msg.dirs)
		if msg.err != nil {
			model.setError(msg.err)
		}
		if model.mode == ModeAdd {
			return model, model.addForm.BlinkCmd()
		}
		return model, nil
	case actionResultMsg:
		model.setError(msg.err)
		return model.refresh(), nil
	case tea.WindowSizeMsg:
		model.width = msg.Width
		model.height = msg.Height
		return model, nil
	case tea.PasteMsg:
		return model.handlePaste(msg)
	case clipboardContentMsg:
		return model.handleClipboardAdd(msg)
	case tea.KeyPressMsg:
		return model.handleKey(msg)
	}
	return model, nil
}

func (model Model) Mode() Mode {
	return model.mode
}

func (model Model) Selected() aria2.Download {
	items := model.items()
	if len(items) == 0 || model.selected < 0 || model.selected >= len(items) {
		return aria2.Download{}
	}
	return items[model.selected]
}

func (model Model) refresh() Model {
	snapshot, err := model.service.ListDownloads(context.Background(), aria2.ListOptions{
		WaitingLimit:  100,
		StoppedOffset: model.stoppedPage * model.stoppedLimit,
		StoppedLimit:  model.stoppedLimit,
	})
	model.loaded = true
	if err != nil {
		model.setError(err)
		return model
	}
	previous := model.Selected().GID
	model.snapshot = snapshot
	model.err = nil
	model.selected = model.indexOf(previous)
	return model
}

func (model Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Input guard: in input modes, text-producing key presses are always
	// treated as typed input and never reach a mode handler's shortcut
	// switch. This structurally prevents single-char shortcuts (e.g. "q" to
	// quit) from firing while a text field is focused, without each handler
	// opting out. Modified combos (ctrl+c) and special keys (esc, enter,
	// tab, ...) have no text payload and reach the mode handlers normally.
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

// handleInputTextKey routes a text-producing key to the focused text field of
// the current input mode. It is the only path by which direct text entry
// reaches input fields, keeping field routing out of handleKey.
func (model Model) handleInputTextKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if model.mode == ModeAdd {
		return model.applyAddForm(model.addForm.HandleKey(msg))
	}
	return model, nil
}

func (model Model) handlePaste(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	if !isInputMode(model.mode) {
		return model, nil
	}
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
		return model, readClipboardCommand()
	case key.Matches(msg, dashboardKeys.List.Add):
		model.mode = ModeAdd
		model.addForm = NewAddForm(model.service.DefaultDir())
		return model, loadRecentDirs(model.service)
	case key.Matches(msg, dashboardKeys.List.Pause):
		return model, model.runSelectedCmd(func(ctx context.Context, gid string) error {
			return model.service.Pause(ctx, gid)
		})
	case key.Matches(msg, dashboardKeys.List.Resume):
		return model, model.runSelectedCmd(func(ctx context.Context, gid string) error {
			return model.service.Resume(ctx, gid)
		})
	case key.Matches(msg, dashboardKeys.List.Remove):
		selected := model.Selected()
		if selected.GID != "" && isStopped(selected) {
			gid := selected.GID
			return model, func() tea.Msg {
				return actionResultMsg{err: model.service.ClearStopped(context.Background(), gid)}
			}
		}
		return model, model.runSelectedCmd(func(ctx context.Context, gid string) error {
			return model.service.Remove(ctx, gid)
		})
	case key.Matches(msg, dashboardKeys.List.NextPage):
		model.stoppedPage++
		return model.refresh(), nil
	case key.Matches(msg, dashboardKeys.List.PrevPage):
		if model.stoppedPage > 0 {
			model.stoppedPage--
		}
		return model.refresh(), nil
	case key.Matches(msg, dashboardKeys.List.Detail):
		model = model.openDetailAt(model.selected)
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
		model.mode = ModeList
		return model, nil
	case AddFormSubmit:
		uri, dir := model.addForm.Values()
		if uri != "" {
			opts := aria2.AddOptions{}
			if dir != "" {
				opts.Dir = dir
			}
			_, err := model.service.AddURI(context.Background(), uri, opts)
			model.setError(err)
		}
		model.addForm = model.addForm.Reset()
		model.mode = ModeList
		return model, nil
	default:
		return model, cmd
	}
}

func (model Model) handleDetailKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, dashboardKeys.Detail.Quit):
		return model, tea.Quit
	case key.Matches(msg, dashboardKeys.Detail.Back):
		model.mode = ModeList
	case key.Matches(msg, dashboardKeys.Detail.Next):
		model = model.openDetailAt(model.selected + 1)
	case key.Matches(msg, dashboardKeys.Detail.Prev):
		model = model.openDetailAt(model.selected - 1)
	case key.Matches(msg, dashboardKeys.Detail.ScrollDown):
		model.detailScroll++
	case key.Matches(msg, dashboardKeys.Detail.ScrollUp):
		if model.detailScroll > 0 {
			model.detailScroll--
		}
	case key.Matches(msg, dashboardKeys.Detail.NextPage):
		page := max(model.height/2, 5)
		model.detailScroll += page
	case key.Matches(msg, dashboardKeys.Detail.PrevPage):
		page := max(model.height/2, 5)
		model.detailScroll -= page
		if model.detailScroll < 0 {
			model.detailScroll = 0
		}
	case key.Matches(msg, dashboardKeys.Detail.Open):
		target := downloadTargetPath(model.detail)
		if target == "" {
			break
		}
		model.setError(openInFileManager(target))
	}
	return model, nil
}

func openInFileManager(target string) error {
	info, err := os.Stat(target)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		dir := filepath.Dir(target)
		if dir == "" || dir == "." {
			return fmt.Errorf("download path is unavailable: %s", target)
		}
		return openInFileManagerPath(dir, true)
	}
	return openInFileManagerPath(target, info.IsDir())
}

func openInFileManagerPath(target string, isDir bool) error {
	switch runtimeGOOS {
	case "darwin":
		if isDir {
			return startExternalCommand("open", target)
		}
		return startExternalCommand("open", "-R", target)
	case "linux":
		dir := target
		if !isDir {
			dir = filepath.Dir(target)
		}
		if dir == "" || dir == "." {
			return fmt.Errorf("download path is unavailable: %s", target)
		}
		return startExternalCommand("xdg-open", dir)
	default:
		return fmt.Errorf("opening downloads is unsupported on %s", runtimeGOOS)
	}
}

func (model Model) openDetailAt(index int) Model {
	items := model.items()
	if index < 0 || index >= len(items) {
		return model
	}
	detail, err := model.service.TaskDetail(context.Background(), items[index].GID)
	if err != nil {
		model.setError(err)
		return model
	}
	model.selected = index
	model.detail = detail
	model.detailScroll = 0
	model.err = nil
	model.mode = ModeDetail
	return model
}

func (model *Model) setError(err error) {
	model.err = err
	if err != nil {
		model.errorInfo = err.Error()
		model.errorInfoTime = time.Now()
	}
}

const errorInfoDuration = 2 * time.Second

// ErrorInfo returns the last error message if it was set within errorInfoDuration.
func (model Model) ErrorInfo() string {
	if model.errorInfo != "" && time.Since(model.errorInfoTime) < errorInfoDuration {
		return model.errorInfo
	}
	return ""
}

// runSelectedCmd returns a tea.Cmd that executes the action asynchronously and
// delivers the result as an actionResultMsg so the UI stays responsive.
func (model Model) runSelectedCmd(action func(context.Context, string) error) tea.Cmd {
	selected := model.Selected()
	if selected.GID == "" {
		return nil
	}
	gid := selected.GID
	return func() tea.Msg {
		return actionResultMsg{err: action(context.Background(), gid)}
	}
}

func (model Model) items() []aria2.Download {
	items := make([]aria2.Download, 0, len(model.snapshot.Active)+len(model.snapshot.Waiting)+len(model.snapshot.Stopped))
	appendBucket := func(downloads []aria2.Download, wantMetadata bool) {
		for _, download := range downloads {
			if download.IsMetadata == wantMetadata {
				items = append(items, download)
			}
		}
	}

	// Metadata rows render as their own status, so keep them in one contiguous
	// bucket instead of splitting them across aria2's active/waiting/stopped
	// windows.
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
	for index, item := range items {
		if item.GID == gid {
			return index
		}
	}
	if model.selected >= len(items) {
		return len(items) - 1
	}
	return model.selected
}

func isStopped(download aria2.Download) bool {
	return download.Status == "complete" || download.Status == "error" || download.Status == "removed"
}

func tick(interval time.Duration) tea.Cmd {
	return tea.Tick(interval, func(time.Time) tea.Msg {
		return refreshMsg{}
	})
}

const loadingTickInterval = 80 * time.Millisecond

func loadingTick() tea.Cmd {
	return tea.Tick(loadingTickInterval, func(time.Time) tea.Msg {
		return loadingTickMsg{}
	})
}

func (model Model) handleClipboardAdd(msg clipboardContentMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		model.setError(msg.err)
		return model, nil
	}

	// Use the most recent directory as the download target.
	dirs, err := model.service.RecentDirs(context.Background())
	lastDir := model.service.DefaultDir()
	if err == nil && len(dirs) > 0 {
		lastDir = dirs[0]
	}

	opts := aria2.AddOptions{}
	if lastDir != "" {
		opts.Dir = lastDir
	}

	_, err = model.service.AddURI(context.Background(), msg.uri, opts)
	model.setError(err)
	return model, nil
}

func readClipboardCommand() tea.Cmd {
	return func() tea.Msg {
		content, err := readClipboardContent()
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

func readClipboardContent() (string, error) {
	switch runtimeGOOS {
	case "darwin":
		data, err := exec.Command("pbpaste").Output()
		if err != nil {
			return "", fmt.Errorf("read clipboard: %w", err)
		}
		return string(data), nil
	case "linux":
		data, err := exec.Command("xclip", "-selection", "clipboard", "-o").Output()
		if err != nil {
			return "", fmt.Errorf("read clipboard: %w", err)
		}
		return string(data), nil
	default:
		return "", fmt.Errorf("clipboard not supported on %s", runtimeGOOS)
	}
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

func loadRecentDirs(service Service) tea.Cmd {
	return func() tea.Msg {
		dirs, err := service.RecentDirs(context.Background())
		return recentDirsMsg{dirs: dirs, err: err}
	}
}

// downloadTargetPath returns the on-disk path for the downloaded content.
// For single-file downloads it returns the file path; for multi-file
// torrents it returns the content directory.
func downloadTargetPath(detail aria2.DownloadDetail) string {
	// Single file: prefer the file's absolute path.
	if len(detail.Files) == 1 && detail.Files[0].Path != "" {
		return detail.Files[0].Path
	}
	// Multi-file or unknown: build from DownloadDir + Name.
	if detail.DownloadDir != "" && detail.Name != "" {
		return filepath.Join(detail.DownloadDir, detail.Name)
	}
	return ""
}

func waitForWSEvent(ch <-chan aria2.Notification) tea.Cmd {
	return func() tea.Msg {
		notif, ok := <-ch
		if !ok {
			return wsDisconnectedMsg{}
		}
		return wsEventMsg{notification: notif}
	}
}
