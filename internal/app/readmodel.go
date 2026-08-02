// Package app owns dashboard task-state projection. Public states reuse aria2
// names except for the user-relevant downloading, metadata, and seeding
// substates of native active work; managed phases remain internal facts.
package app

import "github.com/amio/aria2s/internal/jobs"

type TaskStatus string
type TaskOwnership string
type TaskAction string
type ManagedLifecycle string

const (
	StatusDownloading   TaskStatus       = "downloading"
	StatusSeeding       TaskStatus       = "seeding"
	StatusMetadata      TaskStatus       = "metadata"
	StatusWaiting       TaskStatus       = "waiting"
	StatusPaused        TaskStatus       = "paused"
	StatusComplete      TaskStatus       = "complete"
	StatusError         TaskStatus       = "error"
	StatusRemoved       TaskStatus       = "removed"
	OwnershipManaged    TaskOwnership    = "managed"
	OwnershipUnmanaged  TaskOwnership    = "unmanaged"
	LifecyclePending    ManagedLifecycle = "pending"
	LifecycleStaged     ManagedLifecycle = "staged"
	LifecyclePublishing ManagedLifecycle = "publishing"
	LifecyclePublished  ManagedLifecycle = "published"
	LifecycleRemoved    ManagedLifecycle = "removed"
)

type ClassificationFact struct {
	Managed          bool
	Lifecycle        ManagedLifecycle
	Intent           jobs.ActivityIntent
	IssueCode        string
	NativeStatus     string
	NativeSeeder     bool
	NativeMetadata   bool
	NativeAbsent     bool
	IdentityConflict bool
}

type TaskClassification struct {
	Status    TaskStatus
	Ownership TaskOwnership
	Actions   []TaskAction
}

func ClassifyTask(fact ClassificationFact) TaskClassification {
	result := TaskClassification{Ownership: OwnershipUnmanaged}
	issueIsError := false
	if fact.IssueCode != "" {
		metadata, known := jobs.LookupIssue(fact.IssueCode)
		issueIsError = !known || metadata.Severity == "error"
	}
	if fact.Managed {
		result.Ownership = OwnershipManaged
	}
	switch {
	case fact.Managed && fact.IdentityConflict:
		result.Status = StatusError
	case fact.Managed && fact.Lifecycle == LifecycleRemoved && issueIsError:
		result.Status = StatusError
	case fact.Managed && fact.Lifecycle == LifecycleRemoved:
		result.Status = StatusRemoved
	case fact.Managed && issueIsError:
		result.Status = StatusError
	case fact.Managed && fact.Lifecycle == LifecyclePublishing && fact.NativeAbsent:
		result.Status = StatusError
	case fact.NativeStatus == "active" && fact.NativeMetadata:
		result.Status = StatusMetadata
	case fact.NativeStatus == "active" && fact.NativeSeeder:
		result.Status = StatusSeeding
	case fact.NativeStatus == "active":
		result.Status = StatusDownloading
	case fact.NativeStatus == "waiting":
		result.Status = StatusWaiting
	case fact.NativeStatus == "paused":
		result.Status = StatusPaused
	case fact.Managed && fact.Lifecycle == LifecycleStaged && fact.NativeStatus == "complete" && fact.NativeMetadata:
		result.Status = StatusMetadata
	case fact.NativeStatus == "complete":
		result.Status = StatusComplete
	case fact.NativeStatus == "error":
		result.Status = StatusError
	case fact.NativeStatus == "removed":
		result.Status = StatusRemoved
	case fact.Managed && fact.Intent == jobs.ActivityStopped && fact.Lifecycle != LifecyclePublished:
		result.Status = StatusPaused
	case fact.Managed && fact.Intent == jobs.ActivityStopped && fact.Lifecycle == LifecyclePublished:
		result.Status = StatusComplete
	case fact.Managed && fact.Lifecycle == LifecyclePublishing:
		result.Status = StatusDownloading
	default:
		result.Status = StatusError
	}
	return result
}
