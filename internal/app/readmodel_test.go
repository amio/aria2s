package app

import (
	"reflect"
	"slices"
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

func TestProjectTaskOwnsStatusIssueAndActions(t *testing.T) {
	tests := []struct {
		name        string
		facts       TaskFacts
		wantStatus  TaskStatus
		wantOwner   TaskOwnership
		wantIssue   string
		wantActions []string
	}{
		{name: "active download", facts: TaskFacts{NativeStatus: "active"}, wantStatus: StatusDownloading, wantOwner: OwnershipUnmanaged, wantActions: []string{"pause", "remove"}},
		{name: "active seed", facts: TaskFacts{NativeStatus: "active", NativeSeeder: true}, wantStatus: StatusSeeding, wantOwner: OwnershipUnmanaged, wantActions: []string{"pause", "remove"}},
		{name: "active metadata", facts: TaskFacts{NativeStatus: "active", NativeMetadata: true}, wantStatus: StatusMetadata, wantOwner: OwnershipUnmanaged, wantActions: []string{"pause", "remove"}},
		{name: "waiting", facts: TaskFacts{NativeStatus: "waiting"}, wantStatus: StatusWaiting, wantOwner: OwnershipUnmanaged, wantActions: []string{"pause", "remove"}},
		{name: "paused", facts: TaskFacts{NativeStatus: "paused"}, wantStatus: StatusPaused, wantOwner: OwnershipUnmanaged, wantActions: []string{"resume", "remove"}},
		{name: "managed completed metadata awaiting promotion", facts: TaskFacts{Managed: true, Lifecycle: LifecycleStaged, NativeStatus: "complete", NativeMetadata: true}, wantStatus: StatusMetadata, wantOwner: OwnershipManaged, wantActions: []string{"pause", "remove"}},
		{name: "unmanaged completed metadata", facts: TaskFacts{NativeStatus: "complete", NativeMetadata: true}, wantStatus: StatusComplete, wantOwner: OwnershipUnmanaged, wantActions: []string{"remove"}},
		{name: "managed complete can seed", facts: TaskFacts{Managed: true, Lifecycle: LifecyclePublished, NativeStatus: "complete", CanStartSeeding: true}, wantStatus: StatusComplete, wantOwner: OwnershipManaged, wantActions: []string{"reseed", "remove"}},
		{name: "managed complete cannot seed", facts: TaskFacts{Managed: true, Lifecycle: LifecyclePublished, NativeStatus: "complete"}, wantStatus: StatusComplete, wantOwner: OwnershipManaged, wantActions: []string{"remove"}},
		{name: "error", facts: TaskFacts{NativeStatus: "error"}, wantStatus: StatusError, wantOwner: OwnershipUnmanaged, wantActions: []string{"retry", "remove"}},
		{name: "native removed is cleanup error", facts: TaskFacts{NativeStatus: "removed"}, wantStatus: StatusError, wantOwner: OwnershipUnmanaged, wantActions: []string{"remove"}},
		{name: "managed publishing fallback", facts: TaskFacts{Managed: true, Lifecycle: LifecyclePublishing}, wantStatus: StatusDownloading, wantOwner: OwnershipManaged, wantActions: []string{"remove"}},
		{name: "detached managed publishing recovery", facts: TaskFacts{Managed: true, Lifecycle: LifecyclePublishing, NativeAbsent: true}, wantStatus: StatusError, wantOwner: OwnershipManaged, wantIssue: "PublicationRecoveryRequired", wantActions: []string{"retry", "remove"}},
		{name: "managed staged stop fallback", facts: TaskFacts{Managed: true, Lifecycle: LifecycleStaged, Intent: jobs.ActivityStopped}, wantStatus: StatusPaused, wantOwner: OwnershipManaged, wantActions: []string{"resume", "remove"}},
		{name: "managed published stop fallback", facts: TaskFacts{Managed: true, Lifecycle: LifecyclePublished, Intent: jobs.ActivityStopped}, wantStatus: StatusComplete, wantOwner: OwnershipManaged, wantActions: []string{"remove"}},
		{name: "managed removal transaction", facts: TaskFacts{Managed: true, Lifecycle: LifecycleRemoved, NativeStatus: "active"}, wantStatus: StatusError, wantOwner: OwnershipManaged, wantActions: []string{"remove"}},
		{name: "managed error issue override", facts: TaskFacts{Managed: true, Lifecycle: LifecycleStaged, IssueCode: "RestartStateMissing", NativeStatus: "active"}, wantStatus: StatusError, wantOwner: OwnershipManaged, wantIssue: "RestartStateMissing", wantActions: []string{"retry", "remove"}},
		{name: "managed warning issue keeps catalog actions", facts: TaskFacts{Managed: true, Lifecycle: LifecyclePublished, IssueCode: "CleanupFailed", NativeStatus: "complete", CanStartSeeding: true}, wantStatus: StatusComplete, wantOwner: OwnershipManaged, wantIssue: "CleanupFailed", wantActions: []string{"remove"}},
		{name: "warning without override keeps status actions", facts: TaskFacts{Managed: true, Lifecycle: LifecyclePublished, IssueCode: "PowerLossDurabilityUnavailable", NativeStatus: "complete"}, wantStatus: StatusComplete, wantOwner: OwnershipManaged, wantIssue: "PowerLossDurabilityUnavailable", wantActions: []string{"remove"}},
		{name: "managed identity override", facts: TaskFacts{Managed: true, Lifecycle: LifecycleStaged, IdentityConflict: true, NativeStatus: "active"}, wantStatus: StatusError, wantOwner: OwnershipManaged, wantActions: []string{"retry", "remove"}},
		{name: "observed activity overrides stopped intent", facts: TaskFacts{Managed: true, Lifecycle: LifecycleStaged, Intent: jobs.ActivityStopped, NativeStatus: "active"}, wantStatus: StatusDownloading, wantOwner: OwnershipManaged, wantActions: []string{"pause", "remove"}},
		{name: "corrupt manifest has no destructive action", facts: TaskFacts{Managed: true, NativeAbsent: true, IssueCode: "CorruptManifest"}, wantStatus: StatusError, wantOwner: OwnershipManaged, wantIssue: "CorruptManifest", wantActions: []string{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ProjectTask(test.facts)
			if got.Status != test.wantStatus || got.Ownership != test.wantOwner || got.IssueCode != test.wantIssue || !slices.Equal(got.Actions, test.wantActions) {
				t.Fatalf("projection = %+v, want status=%q owner=%q issue=%q actions=%v", got, test.wantStatus, test.wantOwner, test.wantIssue, test.wantActions)
			}
			if test.wantIssue != "" && got.IssueText == "" {
				t.Fatalf("issue %q lost catalog text: %+v", test.wantIssue, got)
			}
			row := TaskRow{}
			detail := TaskDetail{}
			applyTaskRowProjection(&row, got)
			applyTaskDetailProjection(&detail, got)
			if row.CanonicalStatus != detail.CanonicalStatus || row.Ownership != detail.Ownership || row.IssueCode != detail.IssueCode || row.IssueText != detail.IssueText || !slices.Equal(row.Actions, detail.Actions) {
				t.Fatalf("row/detail projection diverged: row=%+v detail=%+v", row, detail)
			}
		})
	}
}

func TestIssueCatalogActionsAreIsolatedAndDistinguishNoOverride(t *testing.T) {
	metadata, ok := jobs.LookupIssue("FinalSeedStartFailed")
	if !ok || metadata.Severity != "error" || len(metadata.Actions) == 0 {
		t.Fatalf("issue metadata = %+v, ok=%t", metadata, ok)
	}
	metadata.Actions[0] = "mutated"
	again, _ := jobs.LookupIssue("FinalSeedStartFailed")
	if again.Actions[0] == "mutated" {
		t.Fatal("issue metadata action slice was not isolated")
	}
	corrupt, _ := jobs.LookupIssue("CorruptManifest")
	warning, _ := jobs.LookupIssue("PowerLossDurabilityUnavailable")
	if corrupt.Actions == nil || len(corrupt.Actions) != 0 {
		t.Fatalf("corrupt issue must explicitly suppress actions: %#v", corrupt.Actions)
	}
	if warning.Actions != nil {
		t.Fatalf("warning should leave ordinary status actions unchanged: %#v", warning.Actions)
	}
}
