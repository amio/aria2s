package tui

import (
	"strings"
	"testing"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/charmbracelet/x/ansi"
)

func TestTableShowsPeerMetricsAtWideViewport(t *testing.T) {
	const contentWidth = 118
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

func TestStatusLabelsRenderCanonicalStatusWithoutAttributeOverrides(t *testing.T) {
	labels := map[string]string{
		"downloading": "Downloading",
		"metadata":    "Metadata",
		"seeding":     "Seeding",
		"waiting":     "Waiting",
		"paused":      "Paused",
		"complete":    "Complete",
		"error":       "Error",
		"removed":     "Removed",
	}
	for status, want := range labels {
		if got := downloadStatusLabel(aria2.Download{CanonicalStatus: status}); got != want {
			t.Fatalf("canonical status %q rendered as %q, want %q", status, got, want)
		}
	}

	metadataTransfer := aria2.Download{IsMetadata: true, CanonicalStatus: "downloading"}
	if got := downloadStatusLabel(metadataTransfer); got != "Downloading" {
		t.Fatalf("metadata attribute overrode canonical status: %q", got)
	}
	if got := detailStatusLabel(aria2.DownloadDetail{IsMetadata: true, CanonicalStatus: "complete"}); got != "Complete" {
		t.Fatalf("detail metadata attribute overrode canonical status: %q", got)
	}
}

func TestKnownStatusTonesAreDistinct(t *testing.T) {
	statuses := []string{
		"active",
		"downloading",
		"seeding",
		"metadata",
		"waiting",
		"paused",
		"complete",
		"error",
		"removed",
	}
	seen := make(map[rgb]string, len(statuses))
	for _, status := range statuses {
		tone := statusTone(status)
		if other, exists := seen[tone]; exists {
			t.Fatalf("statuses %q and %q share tone %#v", other, status, tone)
		}
		seen[tone] = status
	}
}

func TestTableColumnsHideByPriority(t *testing.T) {
	full := computeLayout(118)
	if full.seedsWidth == 0 || full.peersWidth == 0 {
		t.Fatalf("peer metrics hidden in full layout: %#v", full)
	}
	if full.nameWidth < minNameWidth {
		t.Fatalf("full layout name width got %d, want at least %d", full.nameWidth, minNameWidth)
	}

	peerMetricsHidden := computeLayout(117)
	if peerMetricsHidden.seedsWidth != 0 || peerMetricsHidden.peersWidth != 0 {
		t.Fatalf("equal-priority peer metrics did not hide together: %#v", peerMetricsHidden)
	}
	if peerMetricsHidden.upWidth == 0 || peerMetricsHidden.downWidth == 0 ||
		peerMetricsHidden.downloadedWidth == 0 {
		t.Fatalf("higher-priority columns hid before peer metrics: %#v", peerMetricsHidden)
	}

	upHidden := computeLayout(103)
	if upHidden.upWidth != 0 || upHidden.downWidth == 0 || upHidden.downloadedWidth == 0 {
		t.Fatalf("up speed did not hide at priority 3: %#v", upHidden)
	}

	downHidden := computeLayout(91)
	if downHidden.downWidth != 0 || downHidden.downloadedWidth == 0 {
		t.Fatalf("down speed did not hide at priority 2: %#v", downHidden)
	}

	requiredOnly := computeLayout(minTableWidth)
	if requiredOnly.downloadedWidth != 0 || requiredOnly.downWidth != 0 ||
		requiredOnly.upWidth != 0 ||
		requiredOnly.seedsWidth != 0 || requiredOnly.peersWidth != 0 {
		t.Fatalf("optional columns survived required-only layout: %#v", requiredOnly)
	}
	if requiredOnly.nameWidth != minNameWidth {
		t.Fatalf("minimum name width got %d, want %d", requiredOnly.nameWidth, minNameWidth)
	}
	if requiredOnly.statusWidth == 0 || requiredOnly.sizeWidth == 0 ||
		requiredOnly.progressWidth == 0 {
		t.Fatalf("priority-0 column was hidden: %#v", requiredOnly)
	}
}

func TestTableLayoutPreservesMinimumNameWidth(t *testing.T) {
	for width := minTableWidth; width <= 200; width++ {
		layout := computeLayout(width)
		if layout.nameWidth < minNameWidth {
			t.Fatalf("width %d produced name width %d, want at least %d", width, layout.nameWidth, minNameWidth)
		}
		if got := layout.fixed() + layout.nameWidth; got != width {
			t.Fatalf("width %d produced total layout width %d", width, got)
		}
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
