package tui

import tea "github.com/charmbracelet/bubbletea"

/**
isInputMode reports whether the given mode accepts free-text input.

In input modes, bare-rune shortcuts are suppressed so any character can
be typed into the focused field; modified shortcuts (ctrl+c, alt+x) and
special keys (esc, enter, tab, arrows, backspace) remain active. This is
the single source of truth for which modes trigger input-mode key
guarding in handleKey.
*/
func isInputMode(mode Mode) bool {
	return mode == ModeAdd
}

/**
isBareRune reports whether the key is a single printable rune with no
modifier held — a candidate single-char shortcut such as "q" or "a".

Modified combos and special keys are not bare runes and therefore bypass
input-mode shortcut suppression:
  - ctrl+<key> arrives as its own KeyType (e.g. KeyCtrlC), not KeyRunes;
  - alt+<key> keeps Type == KeyRunes but sets Alt = true;
  - esc/enter/tab/backspace/arrows are distinct non-rune KeyTypes.

Pasted text (Paste = true) is also classified as bare runes so it flows
into the focused field as typed input rather than triggering shortcuts.
*/
func isBareRune(key tea.KeyMsg) bool {
	return key.Type == tea.KeyRunes && !key.Alt
}
