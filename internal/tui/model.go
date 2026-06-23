package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/amio/aria2s/internal/aria2"
)

/** Service is the state-backed task API used by the interactive console. */
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

/** Mode identifies the current console interaction surface. */
type Mode string

const (
	ModeList   Mode = "list"
	ModeAdd    Mode = "add"
	ModeDetail Mode = "detail"
)

/** Model is the Bubble Tea state for the aria2 console. */
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
		return model.refresh(), tea.Batch(
			tick(model.refreshInterval),
			waitForWSEvent(model.wsEvents),
		)
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
	case tea.WindowSizeMsg:
		model.width = msg.Width
		model.height = msg.Height
		return model, nil
	case tea.KeyMsg:
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

func (model Model) handleKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Input guard: in input modes, bare-rune keys are always treated as
	// typed text and never reach a mode handler's shortcut switch. This
	// structurally prevents single-char shortcuts (e.g. "q" to quit) from
	// firing while a text field is focused, without each handler opting
	// out. Modified combos (ctrl+c) and special keys (esc, enter, tab,
	// ...) are not bare runes and reach the mode handlers normally.
	if isInputMode(model.mode) && isBareRune(key) {
		return model.handleInputRune(key)
	}
	switch model.mode {
	case ModeAdd:
		return model.handleAddKey(key)
	case ModeDetail:
		return model.handleDetailKey(key)
	default:
		return model.handleListKey(key)
	}
}

/** handleInputRune routes a bare-rune key to the focused text field of
the current input mode. It is the only path by which typed characters
reach input fields, keeping field routing out of handleKey. */
func (model Model) handleInputRune(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if model.mode == ModeAdd {
		return model.applyAddForm(model.addForm.HandleKey(key))
	}
	return model, nil
}

func (model Model) handleListKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "ctrl+c", "q":
		return model, tea.Quit
	case "down", "j":
		if model.selected < len(model.items())-1 {
			model.selected++
		}
	case "up", "k":
		if model.selected > 0 {
			model.selected--
		}
	case "a":
		model.mode = ModeAdd
		model.addForm = NewAddForm(model.service.DefaultDir())
		return model, loadRecentDirs(model.service)
	case "p":
		model.runSelected(func(ctx context.Context, gid string) error {
			return model.service.Pause(ctx, gid)
		})
	case "r":
		model.runSelected(func(ctx context.Context, gid string) error {
			return model.service.Resume(ctx, gid)
		})
	case "d":
		selected := model.Selected()
		if selected.GID != "" && isStopped(selected) {
			model.setError(model.service.ClearStopped(context.Background(), selected.GID))
		} else {
			model.runSelected(func(ctx context.Context, gid string) error {
				return model.service.Remove(ctx, gid)
			})
		}
	case "n":
		model.stoppedPage++
		return model.refresh(), nil
	case "b":
		if model.stoppedPage > 0 {
			model.stoppedPage--
		}
		return model.refresh(), nil
	case "enter", "l":
		model = model.openDetailAt(model.selected)
	}
	return model, nil
}

func (model Model) handleAddKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	return model.applyAddForm(model.addForm.HandleKey(key))
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

func (model Model) handleDetailKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "q", "ctrl+c":
		return model, tea.Quit
	case "esc", "h", "enter":
		model.mode = ModeList
	case "j":
		model = model.openDetailAt(model.selected + 1)
	case "k":
		model = model.openDetailAt(model.selected - 1)
	case "down":
		model.detailScroll++
	case "up":
		if model.detailScroll > 0 {
			model.detailScroll--
		}
	case "n":
		page := max(model.height/2, 5)
		model.detailScroll += page
	case "b":
		page := max(model.height/2, 5)
		model.detailScroll -= page
		if model.detailScroll < 0 {
			model.detailScroll = 0
		}
	case "o":
		target := downloadTargetPath(model.detail)
		if target == "" {
			break
		}
		info, err := os.Stat(target)
		if err != nil {
			// Path doesn't exist yet — open the parent directory.
			dir := filepath.Dir(target)
			if dir != "" {
				_ = exec.Command("open", dir).Start()
			}
			break
		}
		if info.IsDir() {
			_ = exec.Command("open", target).Start()
		} else {
			_ = exec.Command("open", "-R", target).Start()
		}
	}
	return model, nil
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

func (model *Model) runSelected(action func(context.Context, string) error) {
	selected := model.Selected()
	if selected.GID == "" {
		return
	}
	model.setError(action(context.Background(), selected.GID))
}

func (model Model) items() []aria2.Download {
	items := make([]aria2.Download, 0, len(model.snapshot.Active)+len(model.snapshot.Waiting)+len(model.snapshot.Stopped))
	items = append(items, model.snapshot.Active...)
	items = append(items, model.snapshot.Waiting...)
	items = append(items, model.snapshot.Stopped...)
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