package tui

import (
	"strings"
	"testing"
)

func TestTextFieldDoesNotResetStylesMidLine(t *testing.T) {
	cases := []TextField{
		{
			Label:       "Dir",
			Placeholder: "/downloads (default)",
			Focused:     true,
		},
		{
			Label:   "URL",
			Value:   "magnet:?xt",
			Focused: true,
		},
	}
	for _, field := range cases {
		for _, visible := range []bool{true, false} {
			line := field.Line(visible)
			if strings.Contains(line, "\x1b[0m") {
				t.Fatalf("field line must not use full reset: %q", line)
			}
		}
	}
}

func TestTextFieldPlaceholderCursorDiffersByVisibility(t *testing.T) {
	field := TextField{
		Label:       "Dir",
		Placeholder: "/downloads (default)",
		Focused:     true,
	}
	if field.Line(true) == field.Line(false) {
		t.Fatal("placeholder cursor should change line styling when blinking")
	}
}