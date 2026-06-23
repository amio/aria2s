package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestAddFormTabCyclesRecents(t *testing.T) {
	form := NewAddForm("/home/user/Downloads").WithRecents([]string{
		"/data/Movies",
		"/data/Music",
	})

	form, _, _ = stepKey(form, tea.KeyMsg{Type: tea.KeyTab}) // URL -> Dir
	form, _, _ = stepKey(form, tea.KeyMsg{Type: tea.KeyTab}) // pick 1st
	if form.dir != "/data/Movies" {
		t.Fatalf("first tab got %q, want /data/Movies", form.dir)
	}
	form, _, _ = stepKey(form, tea.KeyMsg{Type: tea.KeyTab}) // pick 2nd
	if form.dir != "/data/Music" {
		t.Fatalf("second tab got %q, want /data/Music", form.dir)
	}
}

func TestAddFormSubmitReturnsTrimmedValues(t *testing.T) {
	form := NewAddForm("")
	form.url = "  https://example.com  "
	form.dir = "  /data/Movies  "

	form, _, action := stepKey(form, tea.KeyMsg{Type: tea.KeyEnter})
	if action != AddFormSubmit {
		t.Fatalf("action got %v, want submit", action)
	}
	uri, dir := form.Values()
	if uri != "https://example.com" || dir != "/data/Movies" {
		t.Fatalf("values got (%q, %q)", uri, dir)
	}
}

func TestAddFormBodyLinesIncludeRecentsWhenDirFocused(t *testing.T) {
	form := NewAddForm("/home").WithRecents([]string{"/data/Movies"})
	form.focus = focusDir

	lines := form.BodyLines()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Recent dirs") || !strings.Contains(joined, "/data/Movies") {
		t.Fatalf("body lines missing recents: %#v", lines)
	}
}

func stepKey(form AddForm, key tea.KeyMsg) (AddForm, tea.Cmd, AddFormAction) {
	return form.HandleKey(key)
}