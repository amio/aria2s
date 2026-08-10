package app

import (
	"reflect"
	"testing"
	"time"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/jobs"
)

func TestNativeDashboardProjectionPreservesProtocolFacts(t *testing.T) {
	addedAt := time.Unix(123, 0).UTC()
	nativeRow := aria2.Download{
		GID: "native", Status: "active", Dir: "/downloads", Name: "payload", IsMetadata: true,
		CompletedLength: 1, TotalLength: 2, LengthKnown: true, DownloadSpeed: 3, UploadSpeed: 4,
		UploadLength: 5, UploadLengthKnown: true, NumSeeders: 6, Connections: 7, Seeder: true,
		InfoHash: "hash", AddedAt: addedAt,
	}
	wantRow := TaskRow{
		GID: "native", Status: "active", Dir: "/downloads", Name: "payload", IsMetadata: true,
		CompletedLength: 1, TotalLength: 2, LengthKnown: true, DownloadSpeed: 3, UploadSpeed: 4,
		UploadLength: 5, UploadLengthKnown: true, NumSeeders: 6, Connections: 7, Seeder: true,
		InfoHash: "hash", AddedAt: addedAt,
	}
	if got := taskRowFromNative(nativeRow); !reflect.DeepEqual(got, wantRow) {
		t.Fatalf("native row projection = %#v, want %#v", got, wantRow)
	}

	nativeDetail := aria2.DownloadDetail{
		GID: "native", Status: "active", Name: "payload", IsMetadata: true,
		CompletedLength: 1, TotalLength: 2, LengthKnown: true, DownloadSpeed: 3, UploadSpeed: 4,
		UploadLength: 5, VerifiedLength: 6, VerifyIntegrityPending: true, InfoHash: "hash",
		NumSeeders: 7, Seeder: true, PieceLength: 8, NumPieces: 9, PrimaryURI: "https://example.test/payload",
		DownloadDir: "/downloads", Connections: 10, ErrorCode: "11", ErrorMessage: "message",
		Files: []aria2.DownloadFile{{Path: "/downloads/payload", Name: "payload", Length: 12, CompletedLength: 11, Selected: true}},
	}
	wantDetail := TaskDetail{
		GID: "native", Status: "active", Name: "payload", IsMetadata: true,
		CompletedLength: 1, TotalLength: 2, LengthKnown: true, DownloadSpeed: 3, UploadSpeed: 4,
		UploadLength: 5, VerifiedLength: 6, VerifyIntegrityPending: true, InfoHash: "hash",
		NumSeeders: 7, Seeder: true, PieceLength: 8, NumPieces: 9, PrimaryURI: "https://example.test/payload",
		DownloadDir: "/downloads", Connections: 10, ErrorCode: "11", ErrorMessage: "message",
		Files: []TaskFile{{Path: "/downloads/payload", Name: "payload", Length: 12, CompletedLength: 11, Selected: true}},
	}
	if got := taskDetailFromNative(nativeDetail); !reflect.DeepEqual(got, wantDetail) {
		t.Fatalf("native detail projection = %#v, want %#v", got, wantDetail)
	}
}

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
		{name: "managed completed metadata awaiting promotion", fact: ClassificationFact{Managed: true, Lifecycle: LifecycleStaged, NativeStatus: "complete", NativeMetadata: true}, want: StatusMetadata},
		{name: "unmanaged completed metadata", fact: ClassificationFact{NativeStatus: "complete", NativeMetadata: true}, want: StatusComplete},
		{name: "complete", fact: ClassificationFact{NativeStatus: "complete"}, want: StatusComplete},
		{name: "error", fact: ClassificationFact{NativeStatus: "error"}, want: StatusError},
		{name: "removed", fact: ClassificationFact{NativeStatus: "removed"}, want: StatusRemoved},
		{name: "managed publishing fallback", fact: ClassificationFact{Managed: true, Lifecycle: LifecyclePublishing}, want: StatusDownloading},
		{name: "detached managed publishing recovery", fact: ClassificationFact{Managed: true, Lifecycle: LifecyclePublishing, NativeAbsent: true}, want: StatusError},
		{name: "managed staged stop fallback", fact: ClassificationFact{Managed: true, Lifecycle: LifecycleStaged, Intent: jobs.ActivityStopped}, want: StatusPaused},
		{name: "managed published stop fallback", fact: ClassificationFact{Managed: true, Lifecycle: LifecyclePublished, Intent: jobs.ActivityStopped}, want: StatusComplete},
		{name: "managed removal override", fact: ClassificationFact{Managed: true, Lifecycle: LifecycleRemoved, NativeStatus: "active"}, want: StatusRemoved},
		{name: "managed issue override", fact: ClassificationFact{Managed: true, IssueCode: "RestartStateMissing", NativeStatus: "active"}, want: StatusError},
		{name: "managed identity override", fact: ClassificationFact{Managed: true, IdentityConflict: true, NativeStatus: "active"}, want: StatusError},
		{name: "observed activity overrides stopped intent", fact: ClassificationFact{Managed: true, Lifecycle: LifecycleStaged, Intent: jobs.ActivityStopped, NativeStatus: "active"}, want: StatusDownloading},
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
