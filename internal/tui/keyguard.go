package tui

import tea "charm.land/bubbletea/v2"

// isInputMode reports whether the given mode accepts free-text input.
//
// In input modes, text-producing key presses are routed to the focused field
// instead of the global shortcut handlers; modified shortcuts (ctrl+c, alt+x)
// and special keys (esc, enter, tab, arrows, backspace) remain active. This
// is the single source of truth for which modes trigger input-mode key
// guarding in handleKey. Pasted text arrives as tea.PasteMsg in Bubble Tea v2
// and is handled separately.
func isInputMode(mode Mode) bool {
	return mode == ModeAdd
}

// isTextInputKey reports whether the key produces printable text with no alt
// modifier held, making it suitable for direct insertion into a text field.
//
// Modified combos and special keys bypass input-mode shortcut suppression:
//   - ctrl+<key> has text == "" and therefore remains available to mode
//     handlers;
//   - alt+<key> keeps text, but sets ModAlt so it is not treated as plain
//     input;
//   - esc/enter/tab/backspace/arrows have text == "".
func isTextInputKey(key tea.KeyPressMsg) bool {
	return key.Text != "" && !key.Mod.Contains(tea.ModAlt)
}
