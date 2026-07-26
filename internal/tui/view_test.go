package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/charmbracelet/x/ansi"
)

func TestTableShowsPeerMetricsAtWideViewport(t *testing.T) {
	const contentWidth = 155
	model := Model{}
	header := model.tableHeader(contentWidth)
	if !strings.Contains(header, "Seeds") || !strings.Contains(header, "Peers") ||
		!strings.Contains(header, "Uploaded") || !strings.Contains(header, "Added Ago") {
		t.Fatalf("wide header missing optional metrics: %q", header)
	}

	row := stripANSI(model.downloadRow(contentWidth+8, aria2.Download{
		GID:               "a",
		Name:              "torrent",
		NumSeeders:        73,
		Connections:       41,
		UploadLength:      2500,
		UploadLengthKnown: true,
		AddedAt:           time.Now().Add(-2*time.Hour - 5*time.Minute),
	}, false))
	if !strings.Contains(row, "73") || !strings.Contains(row, "41") ||
		!strings.Contains(row, "2.5K") || !strings.Contains(row, "2h 05m") {
		t.Fatalf("wide row missing optional metrics: %q", row)
	}
}

func TestAddedAgeFormattingUsesManagedCreationTime(t *testing.T) {
	now := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		addedAt time.Time
		want    string
	}{
		{name: "unknown", want: "—"},
		{name: "less than minute", addedAt: now.Add(-30 * time.Second), want: "<1m"},
		{name: "minutes", addedAt: now.Add(-42 * time.Minute), want: "42m"},
		{name: "hours", addedAt: now.Add(-7*time.Hour - 3*time.Minute), want: "7h 03m"},
		{name: "days", addedAt: now.Add(-49*time.Hour - 8*time.Minute), want: "2d 01h"},
		{name: "future clock", addedAt: now.Add(time.Hour), want: "<1m"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := formatAddedAge(test.addedAt, now); got != test.want {
				t.Fatalf("formatAddedAge() = %q, want %q", got, test.want)
			}
		})
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

func TestListStatsSummarizesOnlyPresentCanonicalStatuses(t *testing.T) {
	model := Model{}
	model.snapshot.Active = []aria2.Download{
		{GID: "downloading", CanonicalStatus: "downloading"},
		{GID: "seeding", CanonicalStatus: "seeding"},
		{GID: "metadata", CanonicalStatus: "metadata"},
	}
	model.snapshot.Stopped = []aria2.Download{
		{GID: "complete-first", CanonicalStatus: "complete"},
		{GID: "complete-second", CanonicalStatus: "complete"},
	}

	got := model.listStats()
	want := "Total 5 (D1 S1 M1 C2) Down 0/s  Up 0/s"
	if got != want {
		t.Fatalf("listStats() = %q, want %q", got, want)
	}
	if strings.Contains(got, "P0") {
		t.Fatalf("listStats() included an absent status: %q", got)
	}
}

func TestCompleteTaskProgressIsSemanticForUnknownOrZeroLength(t *testing.T) {
	if got := formatTaskProgress(0, 0, "complete"); got != "100.0%" {
		t.Fatalf("complete zero-length progress = %q", got)
	}
	if got := formatTaskBytes(0, false); got != "—" {
		t.Fatalf("unknown task length = %q", got)
	}
	if got := formatTaskBytes(0, true); got != "0" {
		t.Fatalf("known zero task length = %q", got)
	}
	if got := formatTaskProgress(0, 0, "metadata"); got != "0.0%" {
		t.Fatalf("metadata unknown-length progress = %q", got)
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
	full := computeLayout(155)
	if full.uploadedWidth == 0 || full.addedAgoWidth == 0 {
		t.Fatalf("new metrics hidden in full layout: %#v", full)
	}
	if full.nameWidth < minNameWidth {
		t.Fatalf("full layout name width got %d, want at least %d", full.nameWidth, minNameWidth)
	}

	addedAgoHidden := computeLayout(154)
	if addedAgoHidden.addedAgoWidth != 0 || addedAgoHidden.uploadedWidth == 0 {
		t.Fatalf("added age did not hide before uploaded: %#v", addedAgoHidden)
	}

	uploadedHidden := computeLayout(138)
	if uploadedHidden.uploadedWidth != 0 || uploadedHidden.peersWidth == 0 {
		t.Fatalf("uploaded did not hide before peer metrics: %#v", uploadedHidden)
	}

	peerMetricsHidden := computeLayout(122)
	if peerMetricsHidden.seedsWidth != 0 || peerMetricsHidden.peersWidth != 0 {
		t.Fatalf("equal-priority peer metrics did not hide together: %#v", peerMetricsHidden)
	}
	if peerMetricsHidden.upWidth == 0 || peerMetricsHidden.downWidth == 0 ||
		peerMetricsHidden.downloadedWidth == 0 {
		t.Fatalf("higher-priority columns hid before peer metrics: %#v", peerMetricsHidden)
	}

	upHidden := computeLayout(99)
	if upHidden.upWidth != 0 || upHidden.downWidth == 0 || upHidden.downloadedWidth == 0 {
		t.Fatalf("up speed did not hide at priority 3: %#v", upHidden)
	}

	downHidden := computeLayout(84)
	if downHidden.downWidth != 0 || downHidden.downloadedWidth == 0 {
		t.Fatalf("down speed did not hide at priority 2: %#v", downHidden)
	}

	requiredOnly := computeLayout(minTableWidth)
	if requiredOnly.downloadedWidth != 0 || requiredOnly.downWidth != 0 ||
		requiredOnly.upWidth != 0 ||
		requiredOnly.seedsWidth != 0 || requiredOnly.peersWidth != 0 ||
		requiredOnly.uploadedWidth != 0 || requiredOnly.addedAgoWidth != 0 {
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

func TestWideHeaderUsesCompactColumnSpacing(t *testing.T) {
	header := Model{}.tableHeader(155)
	titles := []string{"Size", "Downloaded", "Progress", "Down Speed", "Up Speed", "Seeds", "Peers", "Uploaded", "Added Ago"}
	for i := 0; i < len(titles)-1; i++ {
		left := strings.Index(header, titles[i])
		right := strings.Index(header, titles[i+1])
		gap := right - (left + len(titles[i]))
		if gap > 3 {
			t.Fatalf("gap between %q and %q = %d, want at most 3", titles[i], titles[i+1], gap)
		}
	}
	if got := computeLayout(155).sizeWidth; got != 9 {
		t.Fatalf("size width = %d, want 9", got)
	}
}

func TestTableLayoutPreservesMinimumNameWidth(t *testing.T) {
	for width := minTableWidth; width <= 200; width++ {
		layout := computeLayout(width)
		minimumNameWidth := max(minNameWidth, (width*3+9)/10)
		if layout.nameWidth < minimumNameWidth {
			t.Fatalf("width %d produced name width %d, want at least %d", width, layout.nameWidth, minimumNameWidth)
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
