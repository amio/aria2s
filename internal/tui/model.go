package tui

import (
	"context"
	"strings"
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
}

/** Mode identifies the current console interaction surface. */
type Mode string

const (
	ModeList   Mode = "list"
	ModeAdd    Mode = "add"
	ModeDetail Mode = "detail"
)

/** addField identifies which input is focused in add mode. */
type addField int

const (
	fieldURL addField = iota
	fieldDir
)

/** Model is the Bubble Tea state for the aria2 console. */
type Model struct {
	service         Service
	refreshInterval time.Duration
	mode            Mode
	snapshot        aria2.DownloadSnapshot
	selected        int
	width           int
	height          int
	stoppedPage     int
	stoppedLimit    int
	input           string
	dirInput        string
	addField        addField
	defaultDir      string
	recentDirs      []string
	dirPick         int
	detail          aria2.DownloadDetail
	err             error
}

type refreshMsg struct{}

type recentDirsMsg struct {
	dirs []string
	err  error
}

func NewModel(service Service, refreshInterval time.Duration) Model {
	if refreshInterval <= 0 {
		refreshInterval = time.Second
	}
	return Model{
		service:         service,
		refreshInterval: refreshInterval,
		mode:            ModeList,
		stoppedLimit:    100,
		dirPick:         -1,
	}
}

func (model Model) Init() tea.Cmd {
	return tick(model.refreshInterval)
}

func (model Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case refreshMsg:
		return model.refresh(), tick(model.refreshInterval)
	case recentDirsMsg:
		model.recentDirs = msg.dirs
		if msg.err != nil {
			model.err = msg.err
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
	if err != nil {
		model.err = err
		return model
	}
	previous := model.Selected().GID
	model.snapshot = snapshot
	model.err = nil
	model.selected = model.indexOf(previous)
	return model
}

func (model Model) handleKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch model.mode {
	case ModeAdd:
		return model.handleAddKey(key)
	case ModeDetail:
		return model.handleDetailKey(key)
	default:
		return model.handleListKey(key)
	}
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
		model.input = ""
		model.dirInput = ""
		model.addField = fieldURL
		model.dirPick = -1
		model.defaultDir = model.service.DefaultDir()
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
			model.err = model.service.ClearStopped(context.Background(), selected.GID)
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
	switch key.String() {
	case "q", "ctrl+c":
		return model, tea.Quit
	case "esc":
		model.mode = ModeList
		model.input = ""
		model.dirInput = ""
		model.addField = fieldURL
		model.dirPick = -1
		return model, nil
	}
	switch model.addField {
	case fieldDir:
		return model.handleDirFieldKey(key)
	default:
		return model.handleURLFieldKey(key)
	}
}

func (model Model) handleURLFieldKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyTab:
		model.addField = fieldDir
		model.dirPick = -1
	case tea.KeyEnter:
		return model.submitAdd()
	case tea.KeyBackspace:
		if model.input != "" {
			model.input = model.input[:len(model.input)-1]
		}
	case tea.KeyRunes:
		model.input += string(key.Runes)
	}
	return model, nil
}

func (model Model) handleDirFieldKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyShiftTab:
		model.addField = fieldURL
	case tea.KeyTab:
		model.cycleRecents()
	case tea.KeyEnter:
		return model.submitAdd()
	case tea.KeyBackspace:
		if model.dirInput != "" {
			model.dirInput = model.dirInput[:len(model.dirInput)-1]
		}
		model.dirPick = -1
	case tea.KeyUp:
		model.navigateRecents(false)
	case tea.KeyDown:
		model.navigateRecents(true)
	case tea.KeyRunes:
		model.dirInput += string(key.Runes)
		model.dirPick = -1
	}
	return model, nil
}

func (model *Model) cycleRecents() {
	if len(model.recentDirs) == 0 {
		return
	}
	model.dirPick = (model.dirPick + 1) % len(model.recentDirs)
	model.dirInput = model.recentDirs[model.dirPick]
}

func (model *Model) navigateRecents(down bool) {
	if len(model.recentDirs) == 0 {
		return
	}
	if model.dirPick < 0 {
		if !down {
			return
		}
		model.dirPick = 0
	} else {
		if down {
			model.dirPick = min(model.dirPick+1, len(model.recentDirs)-1)
		} else {
			model.dirPick = max(model.dirPick-1, 0)
		}
	}
	model.dirInput = model.recentDirs[model.dirPick]
}

func (model Model) submitAdd() (tea.Model, tea.Cmd) {
	uri := strings.TrimSpace(model.input)
	if uri != "" {
		opts := aria2.AddOptions{}
		if dir := strings.TrimSpace(model.dirInput); dir != "" {
			opts.Dir = dir
		}
		_, model.err = model.service.AddURI(context.Background(), uri, opts)
	}
	model.input = ""
	model.dirInput = ""
	model.addField = fieldURL
	model.dirPick = -1
	model.mode = ModeList
	return model, nil
}

func (model Model) handleDetailKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "q", "ctrl+c":
		return model, tea.Quit
	case "esc", "h", "enter":
		model.mode = ModeList
	case "down", "j":
		model = model.openDetailAt(model.selected + 1)
	case "up", "k":
		model = model.openDetailAt(model.selected - 1)
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
		model.err = err
		return model
	}
	model.selected = index
	model.detail = detail
	model.err = nil
	model.mode = ModeDetail
	return model
}

func (model *Model) runSelected(action func(context.Context, string) error) {
	selected := model.Selected()
	if selected.GID == "" {
		return
	}
	model.err = action(context.Background(), selected.GID)
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

func loadRecentDirs(service Service) tea.Cmd {
	return func() tea.Msg {
		dirs, err := service.RecentDirs(context.Background())
		return recentDirsMsg{dirs: dirs, err: err}
	}
}
