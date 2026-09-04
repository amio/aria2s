package tui

import (
	"reflect"
	"testing"
	"time"

	"github.com/amio/aria2s/internal/app"
)

func TestSortTaskRowsGroupsPausedByProgressAmongOtherStatuses(t *testing.T) {
	now := time.Now()
	rows := []app.TaskRow{
		{GID: "complete", CanonicalStatus: "complete", Name: "z"},
		{GID: "paused-done", CanonicalStatus: "paused", CompletedLength: 100, TotalLength: 100},
		{GID: "seeding", CanonicalStatus: "seeding", Name: "a"},
		{GID: "waiting", CanonicalStatus: "waiting"},
		{GID: "downloading", CanonicalStatus: "downloading", CompletedLength: 10, TotalLength: 100},
		{GID: "paused-partial", CanonicalStatus: "paused", CompletedLength: 99, TotalLength: 100},
		{GID: "metadata", CanonicalStatus: "metadata", AddedAt: now},
		{GID: "error", CanonicalStatus: "error"},
		{GID: "unknown", CanonicalStatus: ""},
	}

	sortTaskRows(rows)

	want := []string{
		"error", "metadata", "paused-partial", "downloading", "waiting",
		"seeding", "paused-done", "complete", "unknown",
	}
	if got := taskGIDs(rows); !reflect.DeepEqual(got, want) {
		t.Fatalf("group order = %v, want %v", got, want)
	}
}

func TestSortTaskRowsAppliesGroupLocalRules(t *testing.T) {
	now := time.Now()
	rows := []app.TaskRow{
		{GID: "queued-first", CanonicalStatus: "waiting"},
		{GID: "queued-second", CanonicalStatus: "waiting"},
		{GID: "metadata-old", CanonicalStatus: "metadata", AddedAt: now.Add(-time.Hour)},
		{GID: "metadata-new", CanonicalStatus: "metadata", AddedAt: now},
		{GID: "downloading-far", CanonicalStatus: "downloading", CompletedLength: 80, TotalLength: 100},
		{GID: "downloading-near", CanonicalStatus: "downloading", CompletedLength: 20, TotalLength: 100},
		{GID: "downloading-unsized", CanonicalStatus: "downloading"},
		{GID: "paused-low", CanonicalStatus: "paused", CompletedLength: 20, TotalLength: 100},
		{GID: "paused-high", CanonicalStatus: "paused", CompletedLength: 80, TotalLength: 100},
		{GID: "seeding-b", CanonicalStatus: "seeding", Name: "Beta"},
		{GID: "seeding-a", CanonicalStatus: "seeding", Name: "alpha"},
		{GID: "paused-done-b", CanonicalStatus: "paused", Name: "beta", CompletedLength: 100, TotalLength: 100},
		{GID: "paused-done-a", CanonicalStatus: "paused", Name: "alpha", CompletedLength: 100, TotalLength: 100},
	}

	sortTaskRows(rows)

	want := []string{
		"metadata-new", "metadata-old",
		"paused-high", "paused-low",
		"downloading-near", "downloading-far", "downloading-unsized",
		"queued-first", "queued-second",
		"seeding-a", "seeding-b",
		"paused-done-a", "paused-done-b",
	}
	if got := taskGIDs(rows); !reflect.DeepEqual(got, want) {
		t.Fatalf("group-local order = %v, want %v", got, want)
	}
}

func TestDashboardSummaryStatusesListEachStatusOnce(t *testing.T) {
	want := []app.TaskStatus{
		app.StatusError, app.StatusMetadata, app.StatusPaused, app.StatusDownloading,
		app.StatusWaiting, app.StatusSeeding, app.StatusComplete,
	}
	if got := dashboardSummaryStatuses; !reflect.DeepEqual(got, want) {
		t.Fatalf("summary statuses = %v, want %v", got, want)
	}
}
