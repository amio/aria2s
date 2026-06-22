package tui

import (
	"context"
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
	if !strings.Contains(view, "Stopped page 1") || !strings.Contains(view, "n next stopped page") || !strings.Contains(view, "b previous stopped page") {
		t.Fatalf("view should describe stopped paging controls, got:\n%s", view)
	}
}

func TestModelOpensAndClosesDetailView(t *testing.T) {
	service := &fakeService{
		snapshot: aria2.DownloadSnapshot{
			Active: []aria2.Download{{GID: "a1", Name: "active.iso", Status: "active"}},
		},
		detail: aria2.DownloadDetail{
			GID:             "a1",
			Name:            "active.iso",
			Status:          "active",
			PrimaryURI:      "https://example.com/active.iso",
			CompletedLength: 50,
			TotalLength:     100,
		},
	}
	model := NewModel(service, time.Second)
	model = updateModel(t, model, refreshMsg{})
	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEnter})

	if service.detailCalls != 1 {
		t.Fatalf("expected detail call, got %d", service.detailCalls)
	}
	if model.Mode() != ModeDetail {
		t.Fatalf("mode got %s, want detail", model.Mode())
	}
	if !strings.Contains(model.View(), "https://example.com/active.iso") {
		t.Fatalf("detail view missing URI:\n%s", model.View())
	}

	model = updateModel(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.Mode() != ModeList {
		t.Fatalf("mode got %s, want list", model.Mode())
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
	if !strings.Contains(addModel.View(), "q quits") {
		t.Fatalf("add view should mention q quits, got:\n%s", addModel.View())
	}
	_, addCommand := addModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if addCommand == nil {
		t.Fatal("expected q to quit from add mode")
	}

	detailModel := updateModel(t, model, refreshMsg{})
	detailModel = updateModel(t, detailModel, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(detailModel.View(), "q quits") {
		t.Fatalf("detail view should mention q quits, got:\n%s", detailModel.View())
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

type fakeService struct {
	snapshot    aria2.DownloadSnapshot
	detail      aria2.DownloadDetail
	listCalls   int
	listOptions []aria2.ListOptions
	detailCalls int
	added       []string
	paused      []string
	resumed     []string
	removed     []string
	cleared     []string
}

func (service *fakeService) ListDownloads(_ context.Context, options aria2.ListOptions) (aria2.DownloadSnapshot, error) {
	service.listCalls++
	service.listOptions = append(service.listOptions, options)
	return service.snapshot, nil
}

func (service *fakeService) TaskDetail(context.Context, string) (aria2.DownloadDetail, error) {
	service.detailCalls++
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
