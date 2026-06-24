package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

const cursorBlinkInterval = 530 * time.Millisecond

type addFocus int

const (
	focusURL addFocus = iota
	focusDir
)

/** AddFormAction reports a high-level outcome from add-form key handling. */
type AddFormAction int

const (
	AddFormNone AddFormAction = iota
	AddFormSubmit
	AddFormCancel
	AddFormQuit
)

type cursorBlinkMsg struct{}

// AddForm owns the add-download form: two text fields, recent-dir picker,
// focus, and cursor blink. Rendering and input behavior are encapsulated
// here; Model only wires it to mode transitions and RPC.
type AddForm struct {
	url           string
	dir           string
	focus         addFocus
	defaultDir    string
	recentDirs    []string
	dirPick       int
	cursorVisible bool
}

func NewAddForm(defaultDir string) AddForm {
	return AddForm{
		focus:         focusURL,
		defaultDir:    defaultDir,
		dirPick:       -1,
		cursorVisible: true,
	}
}

func (form AddForm) Reset() AddForm {
	return NewAddForm(form.defaultDir)
}

func (form AddForm) WithRecents(dirs []string) AddForm {
	form.recentDirs = dirs
	if form.dir == "" && len(dirs) > 0 {
		form.dir = dirs[0]
		form.dirPick = 0
	}
	return form
}

func (form AddForm) Blink() AddForm {
	form.cursorVisible = !form.cursorVisible
	return form
}

func (form AddForm) BlinkCmd() tea.Cmd {
	return tea.Tick(cursorBlinkInterval, func(time.Time) tea.Msg {
		return cursorBlinkMsg{}
	})
}

func (form AddForm) Values() (uri string, dir string) {
	return strings.TrimSpace(form.url), strings.TrimSpace(form.dir)
}

func (form AddForm) URLField() TextField {
	return TextField{
		Label:   "URL",
		Value:   form.url,
		Focused: form.focus == focusURL,
	}
}

func (form AddForm) DirField() TextField {
	hint := form.defaultDir
	if hint == "" {
		hint = "aria2 default"
	}
	return TextField{
		Label:       "Dir",
		Value:       form.dir,
		Placeholder: hint + " (default)",
		Focused:     form.focus == focusDir,
	}
}

func (form AddForm) BodyLines() []string {
	lines := []string{
		"URL or magnet link, Tab to set dir, Enter to submit.",
		"",
		form.URLField().Line(form.cursorVisible),
		form.DirField().Line(form.cursorVisible),
	}
	if form.focus == focusDir && len(form.recentDirs) > 0 {
		lines = append(lines, "", "Recent dirs (Tab to cycle):")
		for i, dir := range form.recentDirs {
			marker := "  "
			if i == form.dirPick {
				marker = "> "
			}
			lines = append(lines, fmt.Sprintf("%s%d %s", marker, i+1, dir))
		}
	}
	return lines
}

func (form AddForm) HandleKey(msg tea.KeyPressMsg) (AddForm, tea.Cmd, AddFormAction) {
	switch {
	case key.Matches(msg, dashboardKeys.Add.Quit):
		return form, nil, AddFormQuit
	case key.Matches(msg, dashboardKeys.Add.Cancel):
		return form.Reset(), nil, AddFormCancel
	}
	if form.focus == focusDir {
		return form.handleDirKey(msg)
	}
	return form.handleURLKey(msg)
}

func (form AddForm) HandlePaste(content string) (AddForm, tea.Cmd, AddFormAction) {
	text := strings.NewReplacer("\r\n", "", "\n", "", "\r", "").Replace(content)
	if text == "" {
		return form, nil, AddFormNone
	}
	if form.focus == focusDir {
		form.dir += text
		form.dirPick = -1
		return form, nil, AddFormNone
	}
	form.url += text
	return form, nil, AddFormNone
}

func (form AddForm) handleURLKey(msg tea.KeyPressMsg) (AddForm, tea.Cmd, AddFormAction) {
	switch {
	case key.Matches(msg, dashboardKeys.Add.NextField):
		form.focus = focusDir
		form.dirPick = -1
		form.cursorVisible = true
	case key.Matches(msg, dashboardKeys.Add.Submit):
		return form, nil, AddFormSubmit
	case key.Matches(msg, dashboardKeys.Add.Backspace):
		form.url = trimLastRune(form.url)
	default:
		if msg.Text != "" {
			form.url += msg.Text
		}
	}
	return form, nil, AddFormNone
}

func (form AddForm) handleDirKey(msg tea.KeyPressMsg) (AddForm, tea.Cmd, AddFormAction) {
	switch {
	case key.Matches(msg, dashboardKeys.Add.PrevField):
		form.focus = focusURL
		form.cursorVisible = true
	case key.Matches(msg, dashboardKeys.Add.NextField):
		form.cycleRecents()
	case key.Matches(msg, dashboardKeys.Add.Submit):
		return form, nil, AddFormSubmit
	case key.Matches(msg, dashboardKeys.Add.Backspace):
		form.dir = trimLastRune(form.dir)
		form.dirPick = -1
	case key.Matches(msg, dashboardKeys.Add.RecentUp):
		form.navigateRecents(false)
	case key.Matches(msg, dashboardKeys.Add.RecentDown):
		form.navigateRecents(true)
	default:
		if msg.Text != "" {
			form.dir += msg.Text
			form.dirPick = -1
		}
	}
	return form, nil, AddFormNone
}

func trimLastRune(text string) string {
	if text == "" {
		return ""
	}
	_, size := utf8.DecodeLastRuneInString(text)
	return text[:len(text)-size]
}

func (form *AddForm) cycleRecents() {
	if len(form.recentDirs) == 0 {
		return
	}
	form.dirPick = (form.dirPick + 1) % len(form.recentDirs)
	form.dir = form.recentDirs[form.dirPick]
}

func (form *AddForm) navigateRecents(down bool) {
	if len(form.recentDirs) == 0 {
		return
	}
	if form.dirPick < 0 {
		if !down {
			return
		}
		form.dirPick = 0
	} else if down {
		form.dirPick = min(form.dirPick+1, len(form.recentDirs)-1)
	} else {
		form.dirPick = max(form.dirPick-1, 0)
	}
	form.dir = form.recentDirs[form.dirPick]
}
