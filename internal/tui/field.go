package tui

import (
	"strings"
	"unicode/utf8"
)

/** TextField is a labeled single-line input with optional placeholder
and block-cursor rendering. All cursor/ANSI styling is self-contained
so callers never manage escape sequences. */
type TextField struct {
	Label       string
	Value       string
	Placeholder string
	Focused     bool
}

func (field TextField) Line(cursorVisible bool) string {
	label := field.Label + ":"
	if field.Focused {
		label = boldText(label)
	}
	return label + " " + field.valueText(cursorVisible)
}

func (field TextField) valueText(cursorVisible bool) string {
	if field.Value == "" && field.Placeholder != "" {
		return renderPlaceholder(field.Placeholder, field.Focused, cursorVisible)
	}
	value := field.Value
	if field.Focused {
		value += renderCursorCell(cursorVisible)
	}
	return value
}

func renderPlaceholder(text string, focused bool, cursorVisible bool) string {
	if !focused || !cursorVisible {
		return dimText(text)
	}
	first, rest := splitFirstRune(text)
	return highlightChar(first) + dimText(rest)
}

func renderCursorCell(visible bool) string {
	if visible {
		return highlightChar(" ")
	}
	return bodyStyled(" ")
}

func highlightChar(ch string) string {
	var builder strings.Builder
	builder.WriteString("\x1b[38;2;")
	builder.WriteString(rgbCode(bodyColor))
	builder.WriteString("m\x1b[48;2;")
	builder.WriteString(rgbCode(bodyTextColor))
	builder.WriteString("m")
	builder.WriteString(ch)
	builder.WriteString(bodyStylePrefix())
	return builder.String()
}

func bodyStyled(text string) string {
	return bodyStylePrefix() + text
}

func bodyStylePrefix() string {
	return "\x1b[38;2;" + rgbCode(bodyTextColor) + "m\x1b[48;2;" + rgbCode(bodyColor) + "m"
}

func splitFirstRune(text string) (string, string) {
	if text == "" {
		return "", ""
	}
	r, size := utf8.DecodeRuneInString(text)
	return string(r), text[size:]
}