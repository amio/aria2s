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

func TestModelRefreshesDownloadsAndMovesSelection(t *testing.T) {
	service := &fakeService{
		snapshot: aria2.DownloadSnapshot{
			Active:  []aria2.Download{{GID: "a1", Name: "active.iso", Status: "active"}},
			Waiting: []aria2.Download{{GID: "w1", Name: "waiting.iso", Status: "waiting"}},
			Stopped: []aria2.Download{{GID: "s1", Name: "done.iso", Status: "complete"}},
		},
	}
	model := NewModel(service, time.Second)

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
	model := NewModel(service, time.Second)
	model = updateModel(t, model, tea.WindowSizeMsg{Width: 140, Height: 16})
	model = updateModel(t, model, refreshMsg{})

	view := model.View()
	for _, header := range []string{"Status", "Name", "Size", "Downloaded", "Progress", "Down", "Up"} {
		if !strings.Contains(view, header) {
			t.Fatalf("view missing column header %q:\n%s", header, view)
		}
	}
	if !strings.Contains(view, "  Items 3") {
		t.Fatalf("view missing footer stats:\n%s", view)
	}
	if !strings.Contains(view, "Enter/l \x1b[2mDetail\x1b[22m") || !strings.Contains(view, "q \x1b[2mQuit\x1b[22m") {
		t.Fatalf("view missing key help:\n%s", view)
	}
	if !strings.Contains(view, "▀") && !strings.Contains(view, "▄") {
		t.Fatalf("view should use half-block header/footer rendering:\n%s", view)
	}
	if got := strings.Count(view, "\n") + 1; got != 16 {
		t.Fatalf("view should fill the terminal height, got %d lines:\n%s", got, view)
	}
}

func TestModelAddsURIFromInputMode(t *testing.T) {
	service := &fakeService{}
	model := NewModel(service, time.Second)

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("https://example.com/file.zip")})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	if len(service.added) != 1 || service.added[0] != "https://example.com/file.zip" {
		t.Fatalf("unexpected added URIs: %#v", service.added)
	}
	if model.Mode() != ModeList {
		t.Fatalf("mode got %s, want list", model.Mode())
	}
}

func TestModelRunsTaskActionsForSelection(t *testing.T) {
	service := &fakeService{
		snapshot: aria2.DownloadSnapshot{
			Active:  []aria2.Download{{GID: "a1", Name: "active.iso", Status: "active"}},
			Stopped: []aria2.Download{{GID: "s1", Name: "done.iso", Status: "complete"}},
		},
	}
	model := NewModel(service, time.Second)
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
	model := NewModel(service, time.Second)

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
	if !strings.Contains(view, "n \x1b[2mNext Page\x1b[22m") || !strings.Contains(view, "b \x1b[2mPrev Page\x1b[22m") {
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
	model := NewModel(service, time.Second)
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
	model := NewModel(service, time.Second)
	model = updateModel(t, model, refreshMsg{})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})

	if service.detailCalls != 1 {
		t.Fatalf("expected detail call, got %d", service.detailCalls)
	}
	if model.Mode() != ModeDetail {
		t.Fatalf("mode got %s, want detail", model.Mode())
	}
	if !strings.Contains(model.View(), "https://example.com/active.iso") {
		t.Fatalf("detail view missing URI:\n%s", model.View())
	}
	if !strings.Contains(model.View(), "\x1b[1mDownload Dir:\x1b[22m /data/downloads") {
		t.Fatalf("detail view missing bold download directory:\n%s", model.View())
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
	model := NewModel(service, time.Second)
	model = updateModel(t, model, refreshMsg{})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})

	if got := model.Selected().GID; got != "a2" {
		t.Fatalf("selected gid got %q, want a2", got)
	}
	if model.detail.GID != "a2" {
		t.Fatalf("detail gid got %q, want a2", model.detail.GID)
	}
	if !strings.Contains(model.View(), "https://example.com/a2.iso") {
		t.Fatalf("detail view should update to next item:\n%s", model.View())
	}

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if got := model.Selected().GID; got != "a1" {
		t.Fatalf("selected gid got %q, want a1", got)
	}
	if model.detail.GID != "a1" {
		t.Fatalf("detail gid got %q, want a1", model.detail.GID)
	}
	if strings.Join(service.detailRequests, ",") != "a1,a2,a1" {
		t.Fatalf("detail requests got %#v", service.detailRequests)
	}
}

func TestModelQuitsFromAddAndDetailModes(t *testing.T) {
	service := &fakeService{
		snapshot: aria2.DownloadSnapshot{
			Active: []aria2.Download{{GID: "a1", Name: "active.iso", Status: "active"}},
		},
		detail: aria2.DownloadDetail{GID: "a1", Name: "active.iso", Status: "active"},
	}
	model := NewModel(service, time.Second)

	addModel := updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if !strings.Contains(addModel.View(), "Esc/h \x1b[2mBack\x1b[22m") || !strings.Contains(addModel.View(), "q \x1b[2mQuit\x1b[22m") {
		t.Fatalf("add view should mention q Quit, got:\n%s", addModel.View())
	}
	addModel = updateModel(t, addModel, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	if addModel.Mode() != ModeList {
		t.Fatalf("mode got %s, want list", addModel.Mode())
	}
	addModel = updateModel(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	_, addCommand := addModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if addCommand == nil {
		t.Fatal("expected q to quit from add mode")
	}

	detailModel := updateModel(t, model, refreshMsg{})
	detailModel = updateModel(t, detailModel, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(detailModel.View(), "Esc/h \x1b[2mBack\x1b[22m") || !strings.Contains(detailModel.View(), "j/k \x1b[2mNext/Prev\x1b[22m") {
		t.Fatalf("detail view should mention q Quit, got:\n%s", detailModel.View())
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

func (service *fakeService) AddURI(_ context.Context, uri string) (string, error) {
	service.added = append(service.added, uri)
	return "new-gid", nil
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
