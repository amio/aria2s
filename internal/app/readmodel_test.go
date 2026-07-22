package app

import (
	"testing"

	"github.com/amio/aria2s/internal/jobs"
)

func TestCanonicalStatusCountsPartitionVisibleRows(t *testing.T) {
	facts := []ClassificationFact{
		{Managed: true, Phase: jobs.PhaseStaged, NativeStatus: "active"},
		{Managed: true, Phase: jobs.PhasePublished, NativeStatus: "active", NativeSeeder: true},
		{Managed: true, Phase: jobs.PhaseStaged, NativeWaiting: true},
		{Managed: true, Phase: jobs.PhaseStaged, Intent: jobs.ActivityStopped},
		{Managed: true, Phase: jobs.PhasePublished, Intent: jobs.ActivityStopped},
		{Managed: true, ProblemCode: "RestartStateMissing"},
		{Managed: true, Phase: jobs.PhaseRemoved},
	}
	rows := make([]TaskClassification, len(facts))
	for index, fact := range facts {
		rows[index] = ClassifyTask(fact)
	}
	counts := CountStatuses(rows)
	sum := counts.Downloading + counts.Seeding + counts.Queued + counts.Paused + counts.Finished + counts.Error + counts.Removed
	if counts.Visible != sum || sum != 7 {
		t.Fatalf("counts = %+v", counts)
	}
}

func TestCanonicalStatusPrefersObservedActivityAndClassifiesUnmanagedSeeds(t *testing.T) {
	active := ClassifyTask(ClassificationFact{Managed: true, Phase: jobs.PhaseStaged, Intent: jobs.ActivityStopped, NativeStatus: "active"})
	if active.Status != StatusDownloading || active.Phase != string(jobs.PhaseStaged) {
		t.Fatalf("active managed row = %+v", active)
	}
	seed := ClassifyTask(ClassificationFact{NativeStatus: "active", NativeSeeder: true})
	if seed.Status != StatusSeeding || seed.Ownership != OwnershipUnmanaged {
		t.Fatalf("unmanaged seed = %+v", seed)
	}
	paused := ClassifyTask(ClassificationFact{NativeStatus: "paused", NativeWaiting: true})
	if paused.Status != StatusPaused {
		t.Fatalf("paused waiting row = %+v", paused)
	}
}
