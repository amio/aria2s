package app

import (
	"testing"

	"github.com/amio/aria2s/internal/jobs"
)

func TestCanonicalStatusUsesNativeVocabularyAndActiveSubstates(t *testing.T) {
	tests := []struct {
		name string
		fact ClassificationFact
		want TaskStatus
	}{
		{name: "active download", fact: ClassificationFact{NativeStatus: "active"}, want: StatusDownloading},
		{name: "active seed", fact: ClassificationFact{NativeStatus: "active", NativeSeeder: true}, want: StatusSeeding},
		{name: "active metadata", fact: ClassificationFact{NativeStatus: "active", NativeMetadata: true}, want: StatusMetadata},
		{name: "waiting", fact: ClassificationFact{NativeStatus: "waiting"}, want: StatusWaiting},
		{name: "paused", fact: ClassificationFact{NativeStatus: "paused"}, want: StatusPaused},
		{name: "complete", fact: ClassificationFact{NativeStatus: "complete"}, want: StatusComplete},
		{name: "error", fact: ClassificationFact{NativeStatus: "error"}, want: StatusError},
		{name: "removed", fact: ClassificationFact{NativeStatus: "removed"}, want: StatusRemoved},
		{name: "managed publishing fallback", fact: ClassificationFact{Managed: true, Phase: jobs.PhasePublishing}, want: StatusDownloading},
		{name: "managed staged stop fallback", fact: ClassificationFact{Managed: true, Phase: jobs.PhaseStaged, Intent: jobs.ActivityStopped}, want: StatusPaused},
		{name: "managed published stop fallback", fact: ClassificationFact{Managed: true, Phase: jobs.PhasePublished, Intent: jobs.ActivityStopped}, want: StatusComplete},
		{name: "managed removal override", fact: ClassificationFact{Managed: true, Phase: jobs.PhaseRemoved, NativeStatus: "active"}, want: StatusRemoved},
		{name: "managed problem override", fact: ClassificationFact{Managed: true, ProblemCode: "RestartStateMissing", NativeStatus: "active"}, want: StatusError},
		{name: "managed identity override", fact: ClassificationFact{Managed: true, IdentityConflict: true, NativeStatus: "active"}, want: StatusError},
		{name: "observed activity overrides stopped intent", fact: ClassificationFact{Managed: true, Phase: jobs.PhaseStaged, Intent: jobs.ActivityStopped, NativeStatus: "active"}, want: StatusDownloading},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ClassifyTask(test.fact)
			if got.Status != test.want {
				t.Fatalf("classification = %+v, want status %q", got, test.want)
			}
			if test.fact.Managed && got.Ownership != OwnershipManaged {
				t.Fatalf("managed classification lost ownership: %+v", got)
			}
		})
	}
}
