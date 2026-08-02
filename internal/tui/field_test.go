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
			lines := strings.Join(field.Lines(visible), "\n")
			if strings.Contains(lines, "\x1b[0m") {
				t.Fatalf("field lines must not use full reset: %q", lines)
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
	if strings.Join(field.Lines(true), "\n") == strings.Join(field.Lines(false), "\n") {
		t.Fatal("placeholder cursor should change line styling when blinking")
	}
}

func TestTextFieldLinesStackLabelAndIndentedValue(t *testing.T) {
	field := TextField{Label: "Directory", Value: "/downloads"}
	lines := field.Lines(false)
	if len(lines) != 2 || lines[0] != "Directory" || lines[1] != "  /downloads" {
		t.Fatalf("Lines() = %#v", lines)
	}
}
