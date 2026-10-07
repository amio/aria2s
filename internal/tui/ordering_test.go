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
		"error", "waiting", "metadata", "paused-partial", "downloading",
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
		{GID: "seeding-b", CanonicalStatus: "seeding", Name: "Beta", CompletedAt: now},
		{GID: "seeding-a", CanonicalStatus: "seeding", Name: "alpha", CompletedAt: now.Add(-time.Hour)},
		{GID: "paused-done-b", CanonicalStatus: "paused", Name: "beta", CompletedLength: 100, TotalLength: 100},
		{GID: "paused-done-a", CanonicalStatus: "paused", Name: "alpha", CompletedLength: 100, TotalLength: 100},
	}

	sortTaskRows(rows)

	want := []string{
		"queued-first", "queued-second",
		"metadata-new", "metadata-old",
		"paused-high", "paused-low",
		"downloading-near", "downloading-far", "downloading-unsized",
		"seeding-b", "seeding-a",
		"paused-done-a", "paused-done-b",
	}
	if got := taskGIDs(rows); !reflect.DeepEqual(got, want) {
		t.Fatalf("group-local order = %v, want %v", got, want)
	}
}

func TestDashboardSummaryStatusesListEachStatusOnce(t *testing.T) {
	want := []app.TaskStatus{
		app.StatusError, app.StatusWaiting, app.StatusMetadata, app.StatusPaused,
		app.StatusDownloading, app.StatusSeeding, app.StatusComplete,
	}
	if got := dashboardSummaryStatuses; !reflect.DeepEqual(got, want) {
		t.Fatalf("summary statuses = %v, want %v", got, want)
	}
}

func TestSortTaskRowsPreservesNewestFirstCompleteHistory(t *testing.T) {
	rows := []app.TaskRow{
		{GID: "complete-new", CanonicalStatus: "complete", Name: "Zulu"},
		{GID: "seeding", CanonicalStatus: "seeding", Name: "seed"},
		{GID: "complete-middle", CanonicalStatus: "complete", Name: "Alpha"},
		{GID: "waiting", CanonicalStatus: "waiting"},
		{GID: "complete-old", CanonicalStatus: "complete", Name: "Beta"},
	}

	sortTaskRows(rows)

	want := []string{"waiting", "seeding", "complete-new", "complete-middle", "complete-old"}
	if got := taskGIDs(rows); !reflect.DeepEqual(got, want) {
		t.Fatalf("complete history order = %v, want %v", got, want)
	}
}

func TestSortTaskRowsOrdersByCompletionTime(t *testing.T) {
	now := time.Now()
	for _, status := range []string{"complete", "seeding"} {
		t.Run(status, func(t *testing.T) {
			rows := []app.TaskRow{
				{GID: "unknown-first", CanonicalStatus: status},
				{GID: "old", CanonicalStatus: status, Name: "Alpha", CompletedAt: now.Add(-time.Hour), AddedAt: now},
				{GID: "new", CanonicalStatus: status, Name: "Zulu", CompletedAt: now, AddedAt: now.Add(-time.Hour)},
				{GID: "equal", CanonicalStatus: status, Name: "Beta", CompletedAt: now},
				{GID: "unknown-second", CanonicalStatus: status},
			}
			sortTaskRows(rows)
			want := []string{"new", "equal", "old", "unknown-first", "unknown-second"}
			if got := taskGIDs(rows); !reflect.DeepEqual(got, want) {
				t.Fatalf("completion order = %v, want %v", got, want)
			}
		})
	}
}
