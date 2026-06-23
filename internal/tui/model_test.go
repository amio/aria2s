package tui

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/amio/aria2s/internal/app"
	"github.com/amio/aria2s/internal/aria2"
)

func TestAppImplementsConsoleService(t *testing.T) {
	var _ Service = (*app.App)(nil)
}

func TestModelShowsLoadingIndicatorBeforeFirstRefresh(t *testing.T) {
	service := &fakeService{}
	model := NewModel(service, time.Second, "dev")
	model = updateModel(t, model, tea.WindowSizeMsg{Width: 140, Height: 16})

	view := model.View()
	if strings.Contains(view, "No downloads yet") {
		t.Fatalf("view should not show empty-state message before first refresh:\n%s", view)
	}
	if !strings.Contains(view, "Connecting...") {
		t.Fatalf("view should show loading indicator before first refresh:\n%s", view)
	}

	model = updateModel(t, model, refreshMsg{})
	view = model.View()
	if strings.Contains(view, "Connecting...") {
		t.Fatalf("view should stop showing loading indicator after first refresh:\n%s", view)
	}
	if !strings.Contains(view, "No downloads yet") {
		t.Fatalf("view should show empty-state message after first refresh:\n%s", view)
	}
}

func TestModelRefreshesDownloadsAndMovesSelection(t *testing.T) {
	service := &fakeService{
		snapshot: aria2.DownloadSnapshot{
			Active:  []aria2.Download{{GID: "a1", Name: "active.iso", Status: "active"}},
			Waiting: []aria2.Download{{GID: "w1", Name: "waiting.iso", Status: "waiting"}},
			Stopped: []aria2.Download{{GID: "s1", Name: "done.iso", Status: "complete"}},
		},
	}
	model := NewModel(service, time.Second, "dev")

	updated, _ := model.Update(refreshMsg{})
	model = updated.(Model)
	if service.listCalls != 1 {
		t.Fatalf("expected one refresh call, got %d", service.listCalls)
	}
	if got := model.Selected().GID; got != "a1" {
		t.Fatalf("selected gid got %q, want a1", got)
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(Model)
	if got := model.Selected().GID; got != "w1" {
		t.Fatalf("selected gid got %q, want w1", got)
	}
	if !strings.Contains(model.View(), "waiting.iso") {
		t.Fatalf("view should include refreshed downloads, got:\n%s", model.View())
	}
}

func TestModelRendersFullScreenTableLayout(t *testing.T) {
	service := &fakeService{
		snapshot: aria2.DownloadSnapshot{
			Active:  []aria2.Download{{GID: "a1", Name: "active.iso", Status: "active", CompletedLength: 50, TotalLength: 100, DownloadSpeed: 2048, UploadSpeed: 0}},
			Waiting: []aria2.Download{{GID: "w1", Name: "queued.iso", Status: "waiting", CompletedLength: 0, TotalLength: 200}},
			Stopped: []aria2.Download{{GID: "s1", Name: "done.iso", Status: "complete", CompletedLength: 300, TotalLength: 300}},
		},
	}
	model := NewModel(service, time.Second, "dev")
	model = updateModel(t, model, tea.WindowSizeMsg{Width: 140, Height: 16})
	model = updateModel(t, model, refreshMsg{})

	view := model.View()
	for _, header := range []string{"Status", "Name", "Size", "Downloaded", "Progress", "Down Speed", "Up Speed"} {
		if !strings.Contains(view, header) {
			t.Fatalf("view missing column header %q:\n%s", header, view)
		}
	}
	if !strings.Contains(view, "Total 3 (A") {
		t.Fatalf("view missing footer stats:\n%s", view)
	}
	if !strings.Contains(view, "Enter/l \x1b[2mDetail\x1b[22m") || !strings.Contains(view, "q \x1b[2mQuit\x1b[22m") {
		t.Fatalf("view missing key help:\n%s", view)
	}
	if got := strings.Count(view, "\n") + 1; got != 16 {
		t.Fatalf("view should fill the terminal height, got %d lines:\n%s", got, view)
	}
}

func TestModelAddsURIFromInputMode(t *testing.T) {
	service := &fakeService{}
	model := NewModel(service, time.Second, "dev")

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("https://example.com/file.zip")})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	if len(service.added) != 1 || service.added[0] != "https://example.com/file.zip" {
		t.Fatalf("unexpected added URIs: %#v", service.added)
	}
	if len(service.addOpts) != 1 || service.addOpts[0].Dir != "" {
		t.Fatalf("expected empty dir option, got %#v", service.addOpts)
	}
	if model.Mode() != ModeList {
		t.Fatalf("mode got %s, want list", model.Mode())
	}
}

func TestModelAddWithCustomDir(t *testing.T) {
	service := &fakeService{defaultDir: "/home/user/Downloads"}
	model := NewModel(service, time.Second, "dev")

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("https://example.com/file.zip")})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyTab})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/data/Movies")})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateModel(t, model, refreshMsg{})

	if len(service.addOpts) != 1 || service.addOpts[0].Dir != "/data/Movies" {
		t.Fatalf("expected dir /data/Movies, got %#v", service.addOpts)
	}
	if !strings.Contains(model.View(), "No downloads yet") && model.Mode() != ModeList {
		t.Fatalf("mode got %s, want list", model.Mode())
	}
}

func TestModelAddDirRecentPick(t *testing.T) {
	service := &fakeService{
		defaultDir: "/home/user/Downloads",
		recentDirs: []string{"/data/Movies", "/data/Music"},
	}
	model := NewModel(service, time.Second, "dev")

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	model.addForm = model.addForm.WithRecents(service.recentDirs)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("https://example.com/file.zip")})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyTab})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyDown})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	if len(service.addOpts) != 1 || service.addOpts[0].Dir != "/data/Movies" {
		t.Fatalf("expected dir /data/Movies picked from recents, got %#v", service.addOpts)
	}
}

func TestModelAddDirTabCyclesAndWraps(t *testing.T) {
	service := &fakeService{
		recentDirs: []string{"/data/Movies", "/data/Music", "/data/Books"},
	}
	model := NewModel(service, time.Second, "dev")

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	model.addForm = model.addForm.WithRecents(service.recentDirs)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyTab}) // URL -> Dir
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyTab}) // -> 1st recent
	if model.addForm.dir != "/data/Movies" {
		t.Fatalf("first tab got %q, want /data/Movies", model.addForm.dir)
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyTab}) // -> 2nd
	if model.addForm.dir != "/data/Music" {
		t.Fatalf("second tab got %q, want /data/Music", model.addForm.dir)
	}
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyTab}) // -> 3rd
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyTab}) // wrap -> 1st
	if model.addForm.dir != "/data/Movies" {
		t.Fatalf("wrapped tab got %q, want /data/Movies", model.addForm.dir)
	}
}

func TestModelAddPrefillsLastUsedDirOnLoad(t *testing.T) {
	service := &fakeService{
		recentDirs: []string{"/data/Movies", "/data/Music"},
	}
	model := NewModel(service, time.Second, "dev")

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("expected recent dirs load command when entering add mode")
	}
	msg := cmd()
	loaded, _ := model.Update(msg)
	model = loaded.(Model)

	if model.addForm.dir != "/data/Movies" {
		t.Fatalf("dir got %q, want /data/Movies", model.addForm.dir)
	}

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("https://example.com/file.zip")})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	if len(service.addOpts) != 1 || service.addOpts[0].Dir != "/data/Movies" {
		t.Fatalf("expected dir /data/Movies from last used, got %#v", service.addOpts)
	}
}

func TestModelLoadsRecentDirsOnAddMode(t *testing.T) {
	service := &fakeService{
		recentDirs: []string{"/data/Movies", "/data/Music"},
	}
	model := NewModel(service, time.Second, "dev")

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("expected recent dirs load command when entering add mode")
	}
	msg := cmd()
	loaded, _ := model.Update(msg)
	model = loaded.(Model)
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyTab})

	view := model.View()
	if !strings.Contains(view, "/data/Movies") || !strings.Contains(view, "Recent dirs") {
		t.Fatalf("add view should list recent dirs after load, got:\n%s", view)
	}
	if service.recentCalls != 1 {
		t.Fatalf("expected one recent dirs call, got %d", service.recentCalls)
	}
}

func TestModelRunsTaskActionsForSelection(t *testing.T) {
	service := &fakeService{
		snapshot: aria2.DownloadSnapshot{
			Active:  []aria2.Download{{GID: "a1", Name: "active.iso", Status: "active"}},
			Stopped: []aria2.Download{{GID: "s1", Name: "done.iso", Status: "complete"}},
		},
	}
	model := NewModel(service, time.Second, "dev")
	model = updateModel(t, model, refreshMsg{})

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyDown})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})

	if strings.Join(service.paused, ",") != "a1" {
		t.Fatalf("paused got %#v", service.paused)
	}
	if strings.Join(service.resumed, ",") != "a1" {
		t.Fatalf("resumed got %#v", service.resumed)
	}
	if strings.Join(service.removed, ",") != "a1" {
		t.Fatalf("removed got %#v", service.removed)
	}
	if strings.Join(service.cleared, ",") != "s1" {
		t.Fatalf("cleared got %#v", service.cleared)
	}
}

func TestModelPagesStoppedDownloads(t *testing.T) {
	service := &fakeService{
		snapshot: aria2.DownloadSnapshot{
			Stopped: []aria2.Download{{GID: "s1", Name: "done.iso", Status: "complete"}},
		},
	}
	model := NewModel(service, time.Second, "dev")

	model = updateModel(t, model, refreshMsg{})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})

	if len(service.listOptions) != 3 {
		t.Fatalf("expected three list calls, got %d", len(service.listOptions))
	}
	if service.listOptions[0].StoppedOffset != 0 {
		t.Fatalf("initial stopped offset got %d, want 0", service.listOptions[0].StoppedOffset)
	}
	if service.listOptions[1].StoppedOffset != 100 {
		t.Fatalf("next page stopped offset got %d, want 100", service.listOptions[1].StoppedOffset)
	}
	if service.listOptions[2].StoppedOffset != 0 {
		t.Fatalf("previous page stopped offset got %d, want 0", service.listOptions[2].StoppedOffset)
	}
	view := model.View()
	if !strings.Contains(view, "n/b \x1b[2mNext/Prev Page\x1b[22m") {
		t.Fatalf("view should describe stopped paging controls, got:\n%s", view)
	}
}

func TestModelDisplaysMetadataLabelForMetadataEntries(t *testing.T) {
	service := &fakeService{
		snapshot: aria2.DownloadSnapshot{
			Active: []aria2.Download{
				{GID: "m1", Name: "GIRLT.No.017.7z", Status: "active", IsMetadata: true},
				{GID: "a1", Name: "movie.mkv", Status: "active"},
			},
		},
	}
	model := NewModel(service, time.Second, "dev")
	model = updateModel(t, model, tea.WindowSizeMsg{Width: 140, Height: 16})
	model = updateModel(t, model, refreshMsg{})

	view := model.View()
	if !strings.Contains(view, "Metadata") {
		t.Fatalf("view should show 'Metadata' status for metadata entries:\n%s", view)
	}
	if !strings.Contains(view, "GIRLT.No.017.7z") {
		t.Fatalf("view should show metadata entry name:\n%s", view)
	}
}

func TestModelOpensAndClosesDetailView(t *testing.T) {
	service := &fakeService{
		snapshot: aria2.DownloadSnapshot{
			Active: []aria2.Download{{GID: "a1", Name: "active.iso", Status: "active"}},
		},
		detail: withDownloadDir(t, aria2.DownloadDetail{
			GID:             "a1",
			Name:            "active.iso",
			Status:          "active",
			PrimaryURI:      "https://example.com/active.iso",
			Files:           []aria2.DownloadFile{{Path: "/data/downloads/active.iso", Name: "active.iso"}},
			CompletedLength: 50,
			TotalLength:     100,
		}, "/data/downloads"),
	}
	model := NewModel(service, time.Second, "dev")
	model = updateModel(t, model, refreshMsg{})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})

	if service.detailCalls != 1 {
		t.Fatalf("expected detail call, got %d", service.detailCalls)
	}
	if model.Mode() != ModeDetail {
		t.Fatalf("mode got %s, want detail", model.Mode())
	}
	if !strings.Contains(model.View(), "active.iso") {
		t.Fatalf("detail view missing name in header:\n%s", model.View())
	}
	if !strings.Contains(model.View(), "[Active]") {
		t.Fatalf("detail view missing status in header:\n%s", model.View())
	}
	if !strings.Contains(model.View(), "\x1b[2mDownload Dir:") {
		t.Fatalf("detail view missing dim download directory:\n%s", model.View())
	}

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	if model.Mode() != ModeList {
		t.Fatalf("mode got %s, want list", model.Mode())
	}
}

func TestModelNavigatesAdjacentDetailsWithJK(t *testing.T) {
	service := &fakeService{
		snapshot: aria2.DownloadSnapshot{
			Active: []aria2.Download{
				{GID: "a1", Name: "active.iso", Status: "active"},
				{GID: "a2", Name: "queued.iso", Status: "waiting"},
			},
		},
		details: map[string]aria2.DownloadDetail{
			"a1": withDownloadDir(t, aria2.DownloadDetail{
				GID:        "a1",
				Name:       "active.iso",
				Status:     "active",
				PrimaryURI: "https://example.com/a1.iso",
				Files:      []aria2.DownloadFile{{Path: "/downloads/a/active.iso", Name: "active.iso"}},
			}, "/downloads/a"),
			"a2": withDownloadDir(t, aria2.DownloadDetail{
				GID:        "a2",
				Name:       "queued.iso",
				Status:     "waiting",
				PrimaryURI: "https://example.com/a2.iso",
				Files:      []aria2.DownloadFile{{Path: "/downloads/b/queued.iso", Name: "queued.iso"}},
			}, "/downloads/b"),
		},
	}
	model := NewModel(service, time.Second, "dev")
	model = updateModel(t, model, refreshMsg{})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})

	if got := model.Selected().GID; got != "a2" {
		t.Fatalf("selected gid got %q, want a2", got)
	}
	if model.detail.GID != "a2" {
		t.Fatalf("detail gid got %q, want a2", model.detail.GID)
	}
	if !strings.Contains(model.View(), "queued.iso") {
		t.Fatalf("detail view should update to next item:\n%s", model.View())
	}

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if got := model.Selected().GID; got != "a1" {
		t.Fatalf("selected gid got %q, want a1", got)
	}
	if model.detail.GID != "a1" {
		t.Fatalf("detail gid got %q, want a1", model.detail.GID)
	}
}

func TestModelScrollsDetailWithArrows(t *testing.T) {
	service := &fakeService{
		snapshot: aria2.DownloadSnapshot{
			Active: []aria2.Download{{GID: "a1", Name: "active.iso", Status: "active"}},
		},
		detail: withDownloadDir(t, aria2.DownloadDetail{
			GID:        "a1",
			Name:       "active.iso",
			Status:     "active",
			PrimaryURI: "https://example.com/a1.iso",
			Files:      []aria2.DownloadFile{{Path: "/downloads/a/active.iso", Name: "active.iso"}},
		}, "/downloads/a"),
	}
	model := NewModel(service, time.Second, "dev")
	model = updateModel(t, model, refreshMsg{})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	if model.detailScroll != 0 {
		t.Fatalf("scroll got %d, want 0", model.detailScroll)
	}

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyDown})
	if model.detailScroll != 1 {
		t.Fatalf("scroll got %d, want 1 after down", model.detailScroll)
	}
	if model.detail.GID != "a1" {
		t.Fatalf("detail gid changed to %q, down should not switch items", model.detail.GID)
	}

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyUp})
	if model.detailScroll != 0 {
		t.Fatalf("scroll got %d, want 0 after up", model.detailScroll)
	}
}

func TestModelQuitsFromAddAndDetailModes(t *testing.T) {
	service := &fakeService{
		snapshot: aria2.DownloadSnapshot{
			Active: []aria2.Download{{GID: "a1", Name: "active.iso", Status: "active"}},
		},
		detail: aria2.DownloadDetail{GID: "a1", Name: "active.iso", Status: "active"},
	}
	model := NewModel(service, time.Second, "dev")

	addModel := updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if !strings.Contains(addModel.View(), "Esc \x1b[2mBack\x1b[22m") || !strings.Contains(addModel.View(), "Ctrl+C \x1b[2mQuit\x1b[22m") {
		t.Fatalf("add view should mention Ctrl+C Quit, got:\n%s", addModel.View())
	}
	addModel = updateModel(t, addModel, tea.KeyMsg{Type: tea.KeyEsc})
	if addModel.Mode() != ModeList {
		t.Fatalf("mode got %s, want list", addModel.Mode())
	}

	// In input mode, bare runes are typed text and never act as shortcuts:
	// pressing "q" must append to the URL field instead of quitting. Only
	// ctrl+c (a modified combo, not a bare rune) quits from add mode.
	addModel = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	addModel = updateModel(t, addModel, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if addModel.Mode() != ModeAdd {
		t.Fatalf("mode got %s, want add after typing q", addModel.Mode())
	}
	if got := addModel.addForm.url; got != "q" {
		t.Fatalf("input got %q, want q (bare runes must be typed, not shortcuts)", got)
	}
	_, quitCmd := addModel.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if quitCmd == nil {
		t.Fatal("expected ctrl+c to quit from add mode")
	}

	detailModel := updateModel(t, model, refreshMsg{})
	detailModel = updateModel(t, detailModel, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(detailModel.View(), "Esc/h \x1b[2mBack\x1b[22m") || !strings.Contains(detailModel.View(), "j/k \x1b[2mNext/Prev\x1b[22m") {
		t.Fatalf("detail view should mention Esc/h Back and j/k Next/Prev, got:\n%s", detailModel.View())
	}
	_, detailCommand := detailModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if detailCommand == nil {
		t.Fatal("expected q to quit from detail mode")
	}
}

func updateModel(t *testing.T, model Model, msg tea.Msg) Model {
	t.Helper()
	updated, _ := model.Update(msg)
	return updated.(Model)
}

func withDownloadDir(t *testing.T, detail aria2.DownloadDetail, dir string) aria2.DownloadDetail {
	t.Helper()
	field := reflect.ValueOf(&detail).Elem().FieldByName("DownloadDir")
	if !field.IsValid() {
		t.Fatal("DownloadDetail is missing DownloadDir")
	}
	if field.Kind() != reflect.String || !field.CanSet() {
		t.Fatal("DownloadDetail.DownloadDir must be a settable string")
	}
	field.SetString(dir)
	return detail
}

type fakeService struct {
	snapshot       aria2.DownloadSnapshot
	detail         aria2.DownloadDetail
	details        map[string]aria2.DownloadDetail
	listCalls      int
	listOptions    []aria2.ListOptions
	detailCalls    int
	detailRequests []string
	added          []string
	addOpts        []aria2.AddOptions
	defaultDir     string
	recentDirs     []string
	recentCalls    int
	paused         []string
	resumed        []string
	removed        []string
	cleared        []string
}

func (service *fakeService) ListDownloads(_ context.Context, options aria2.ListOptions) (aria2.DownloadSnapshot, error) {
	service.listCalls++
	service.listOptions = append(service.listOptions, options)
	return service.snapshot, nil
}

func (service *fakeService) TaskDetail(_ context.Context, gid string) (aria2.DownloadDetail, error) {
	service.detailCalls++
	service.detailRequests = append(service.detailRequests, gid)
	if service.details != nil {
		if detail, ok := service.details[gid]; ok {
			return detail, nil
		}
	}
	return service.detail, nil
}

func (service *fakeService) AddURI(_ context.Context, uri string, opts aria2.AddOptions) (string, error) {
	service.added = append(service.added, uri)
	service.addOpts = append(service.addOpts, opts)
	return "new-gid", nil
}

func (service *fakeService) RecentDirs(context.Context) ([]string, error) {
	service.recentCalls++
	return service.recentDirs, nil
}

func (service *fakeService) DefaultDir() string {
	return service.defaultDir
}

func (service *fakeService) Pause(_ context.Context, gid string) error {
	service.paused = append(service.paused, gid)
	return nil
}

func (service *fakeService) Resume(_ context.Context, gid string) error {
	service.resumed = append(service.resumed, gid)
	return nil
}

func (service *fakeService) Remove(_ context.Context, gid string) error {
	service.removed = append(service.removed, gid)
	return nil
}

func (service *fakeService) ClearStopped(_ context.Context, gid string) error {
	service.cleared = append(service.cleared, gid)
	return nil
}

func (service *fakeService) Subscribe(context.Context) <-chan aria2.Notification {
	return nil
}
