package tui

import (
	"strings"
	"testing"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/charmbracelet/x/ansi"
)

func TestTableShowsPeerMetricsAtWideViewport(t *testing.T) {
	const contentWidth = 111
	model := Model{}
	header := model.tableHeader(contentWidth)
	if !strings.Contains(header, "Seeds") || !strings.Contains(header, "Peers") {
		t.Fatalf("wide header missing peer metrics: %q", header)
	}

	row := stripANSI(model.downloadRow(contentWidth+8, aria2.Download{
		GID:         "a",
		Name:        "torrent",
		NumSeeders:  73,
		Connections: 41,
	}, false))
	if !strings.Contains(row, "73") || !strings.Contains(row, "41") {
		t.Fatalf("wide row missing peer metrics: %q", row)
	}
}

func TestPeerMetricsHideBeforeExistingOptionalColumns(t *testing.T) {
	full := computeLayout(111)
	if full.seedsWidth == 0 || full.peersWidth == 0 {
		t.Fatalf("peer metrics hidden in full layout: %#v", full)
	}

	oneHidden := computeLayout(110)
	if oneHidden.seedsWidth != 0 || oneHidden.peersWidth == 0 {
		t.Fatalf("first low-priority column was not hidden first: %#v", oneHidden)
	}

	bothHidden := computeLayout(103)
	if bothHidden.seedsWidth != 0 || bothHidden.peersWidth != 0 {
		t.Fatalf("peer metrics survived narrow layout: %#v", bothHidden)
	}
	if bothHidden.downloadedWidth == 0 || bothHidden.sizeWidth == 0 || bothHidden.upWidth == 0 {
		t.Fatalf("existing optional columns hid before peer metrics: %#v", bothHidden)
	}
}

func TestAppendDetailLabelLinesWrapsLongValues(t *testing.T) {
	value := "disk error: insufficient space on volume /very/long/path/to/downloads/folder"
	lines := appendDetailLabelLines(nil, "Error 18", value, 40)

	if len(lines) < 2 {
		t.Fatalf("expected wrapped lines, got %d: %v", len(lines), lines)
	}
	for i, line := range lines {
		if ansi.StringWidth(stripANSI(line)) > 40 {
			t.Fatalf("line %d exceeds width 40: %q", i, line)
		}
	}
	if !strings.Contains(lines[0], "Error 18:") {
		t.Fatalf("first line should include label, got %q", lines[0])
	}
	rejoined := strings.Join(lines, "")
	for _, part := range []string{"disk error:", "insufficient space", "loads/folder"} {
		if !strings.Contains(rejoined, part) {
			t.Fatalf("wrapped output missing %q: %v", part, lines)
		}
	}
}

func TestAppendDetailLabelLinesPreservesShortValues(t *testing.T) {
	lines := appendDetailLabelLines(nil, "GID", "abc123", 80)
	if len(lines) != 1 {
		t.Fatalf("expected one line, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "abc123") {
		t.Fatalf("line should include value, got %q", lines[0])
	}
}

func stripANSI(text string) string {
	return ansi.Strip(text)
}
