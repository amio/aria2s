package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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

/** AddForm owns the add-download form: two text fields, recent-dir
picker, focus, and cursor blink. Rendering and input behavior are
encapsulated here; Model only wires it to mode transitions and RPC. */
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
		Label:  "URL",
		Value:  form.url,
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

func (form AddForm) HandleKey(key tea.KeyMsg) (AddForm, tea.Cmd, AddFormAction) {
	switch key.String() {
	case "ctrl+c":
		return form, nil, AddFormQuit
	case "esc":
		return form.Reset(), nil, AddFormCancel
	}
	if form.focus == focusDir {
		return form.handleDirKey(key)
	}
	return form.handleURLKey(key)
}

func (form AddForm) handleURLKey(key tea.KeyMsg) (AddForm, tea.Cmd, AddFormAction) {
	switch key.Type {
	case tea.KeyTab:
		form.focus = focusDir
		form.dirPick = -1
		form.cursorVisible = true
	case tea.KeyEnter:
		return form, nil, AddFormSubmit
	case tea.KeyBackspace:
		if form.url != "" {
			form.url = form.url[:len(form.url)-1]
		}
	case tea.KeyRunes:
		form.url += string(key.Runes)
	}
	return form, nil, AddFormNone
}

func (form AddForm) handleDirKey(key tea.KeyMsg) (AddForm, tea.Cmd, AddFormAction) {
	switch key.Type {
	case tea.KeyShiftTab:
		form.focus = focusURL
		form.cursorVisible = true
	case tea.KeyTab:
		form.cycleRecents()
	case tea.KeyEnter:
		return form, nil, AddFormSubmit
	case tea.KeyBackspace:
		if form.dir != "" {
			form.dir = form.dir[:len(form.dir)-1]
		}
		form.dirPick = -1
	case tea.KeyUp:
		form.navigateRecents(false)
	case tea.KeyDown:
		form.navigateRecents(true)
	case tea.KeyRunes:
		form.dir += string(key.Runes)
		form.dirPick = -1
	}
	return form, nil, AddFormNone
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

