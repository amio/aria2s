package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/amio/aria2s/internal/app"
	"github.com/charmbracelet/x/ansi"
)

func TestMaskedTitlesRemainPresentationOnly(t *testing.T) {
	model := NewModel(context.Background(), &fakeService{}, time.Second, "dev")
	model.width, model.height = 180, 40
	row := app.TaskRow{GID: "job-1234abcd", Name: "secret.mp4", CanonicalStatus: "downloading"}
	model.list.Snapshot.Active = []app.TaskRow{row}
	model.detailCache[row.GID] = cachedTaskDetail{Detail: app.TaskDetail{
		GID: row.GID, Name: row.Name, CanonicalStatus: row.CanonicalStatus,
	}}
	toggle := tea.KeyPressMsg{Code: 'm', Text: "m"}
	updated, cmd := model.Update(toggle)
	model = updated.(Model)
	if cmd != nil || !model.maskNames {
		t.Fatal("mask toggle should change only local presentation state")
	}
	alias := model.displayTaskName(row.Name, row.GID)
	if len(strings.Fields(alias)) != 2 || !strings.HasSuffix(alias, "-bcd") {
		t.Fatalf("short title should use two words and the task ID suffix: %q", alias)
	}
	if got := model.displayTaskName(row.Name, "job-5678ef01"); strings.TrimSuffix(got, "-f01") != strings.TrimSuffix(alias, "-bcd") {
		t.Fatalf("word selection should depend on the name alone: %q vs %q", got, alias)
	}
	if len(strings.Fields(model.displayTaskName(strings.Repeat("a", 30), row.GID))) <= 2 {
		t.Fatal("long names should produce longer word combinations")
	}
	for _, mode := range []Mode{ModeList, ModeDetail} {
		model.mode = mode
		model.detailState.RequestedGID = row.GID
		view := ansi.Strip(model.View().Content)
		if strings.Contains(view, row.Name) || !strings.Contains(view, alias) {
			t.Fatalf("mode %v did not mask the task title:\n%s", mode, view)
		}
	}
	if model.list.Snapshot.Active[0].Name != row.Name || model.detailCache[row.GID].Detail.Name != row.Name {
		t.Fatal("masking must preserve the source row and detail cache")
	}
	updated, cmd = model.Update(toggle)
	model = updated.(Model)
	if cmd != nil || model.maskNames || !strings.Contains(ansi.Strip(model.View().Content), row.Name) {
		t.Fatal("second toggle should restore the original title")
	}
	for _, help := range [][]helpItem{dashboardKeys.List.HelpItems(nil), dashboardKeys.Detail.HelpItems(), dashboardKeys.Add.HelpItems()} {
		for _, item := range help {
			if item.key == "m" {
				t.Fatal("mask shortcut must stay out of help")
			}
		}
	}
	model.mode = ModeAdd
	updated, _ = model.Update(toggle)
	if updated.(Model).maskNames {
		t.Fatal("typing m in the add form must not toggle masking")
	}
}
