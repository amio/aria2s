package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestAddFormTabAndShiftTabSwitchFields(t *testing.T) {
	form := NewAddForm("/home/user/Downloads").WithRecents([]string{
		"/data/Movies",
		"/data/Music",
	})

	form, _, _ = stepKey(form, keySpecial(tea.KeyTab))
	if form.focus != focusDir {
		t.Fatalf("Tab focus got %v, want directory", form.focus)
	}
	form, _, _ = stepKey(form, keySpecial(tea.KeyTab))
	if form.focus != focusURL {
		t.Fatalf("second Tab focus got %v, want URL", form.focus)
	}
	form, _, _ = stepKey(form, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if form.focus != focusDir {
		t.Fatalf("Shift+Tab focus got %v, want directory", form.focus)
	}
}

func TestAddFormSubmitReturnsTrimmedValues(t *testing.T) {
	form := NewAddForm("")
	form.url = "  https://example.com  "
	form.dir = "  /data/Movies  "

	form, _, action := stepKey(form, keySpecial(tea.KeyEnter))
	if action != AddFormSubmit {
		t.Fatalf("action got %v, want submit", action)
	}
	uri, dir := form.Values()
	if uri != "https://example.com" || dir != "/data/Movies" {
		t.Fatalf("values got (%q, %q)", uri, dir)
	}
}

func TestAddFormWithRecentsPrefillsLastUsedDir(t *testing.T) {
	form := NewAddForm("/home/user/Downloads").WithRecents([]string{
		"/data/Movies",
		"/data/Music",
	})

	if form.dir != "/data/Movies" {
		t.Fatalf("dir got %q, want /data/Movies", form.dir)
	}
	if form.dirPick != 0 {
		t.Fatalf("dirPick got %d, want 0", form.dirPick)
	}
}

func TestAddFormWithRecentsDoesNotOverwriteExistingDir(t *testing.T) {
	form := NewAddForm("/home/user/Downloads")
	form.dir = "/custom/path"
	form = form.WithRecents([]string{"/data/Movies"})

	if form.dir != "/custom/path" {
		t.Fatalf("dir got %q, want existing /custom/path", form.dir)
	}
}

func TestAddFormBodyLinesUseStackedFieldsAndFocusedRecents(t *testing.T) {
	form := NewAddForm("/home").WithRecents([]string{"/data/Movies", "/data/Music"})

	unfocused := strings.Join(form.BodyLines(), "\n")
	if strings.Contains(unfocused, "Recent") {
		t.Fatalf("recents visible while URL focused:\n%s", unfocused)
	}
	if !strings.Contains(unfocused, "Directory\n  /data/Movies") {
		t.Fatalf("fields are not stacked and indented:\n%s", unfocused)
	}

	form.focus = focusDir
	focused := strings.Join(form.BodyLines(), "\n")
	if !strings.Contains(focused, "  Recent (↑↓ select)") ||
		!strings.Contains(focused, "  ›  /data/Movies") ||
		!strings.Contains(focused, "     /data/Music") {
		t.Fatalf("focused recent picker has unexpected layout:\n%s", focused)
	}
}

func TestAddFormArrowsSelectRecentsWithoutChangingFieldFocus(t *testing.T) {
	form := NewAddForm("").WithRecents([]string{"/data/Movies", "/data/Music"})
	form.focus = focusDir

	form, _, _ = stepKey(form, keySpecial(tea.KeyDown))
	if form.dir != "/data/Music" || form.dirPick != 1 || form.focus != focusDir {
		t.Fatalf("down selection got dir=%q pick=%d focus=%v", form.dir, form.dirPick, form.focus)
	}
	form, _, _ = stepKey(form, keySpecial(tea.KeyUp))
	if form.dir != "/data/Movies" || form.dirPick != 0 || form.focus != focusDir {
		t.Fatalf("up selection got dir=%q pick=%d focus=%v", form.dir, form.dirPick, form.focus)
	}
}

func stepKey(form AddForm, key tea.KeyPressMsg) (AddForm, tea.Cmd, AddFormAction) {
	return form.HandleKey(key)
}
