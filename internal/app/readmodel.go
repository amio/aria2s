// Package app owns dashboard task-state projection. Public states reuse aria2
// names except for the user-relevant downloading, metadata, and seeding
// substates of native active work; managed phases remain internal facts.
package app

import "github.com/amio/aria2s/internal/jobs"

type TaskStatus string
type TaskOwnership string
type TaskAction string

const (
	StatusDownloading  TaskStatus    = "downloading"
	StatusSeeding      TaskStatus    = "seeding"
	StatusMetadata     TaskStatus    = "metadata"
	StatusWaiting      TaskStatus    = "waiting"
	StatusPaused       TaskStatus    = "paused"
	StatusComplete     TaskStatus    = "complete"
	StatusError        TaskStatus    = "error"
	StatusRemoved      TaskStatus    = "removed"
	OwnershipManaged   TaskOwnership = "managed"
	OwnershipUnmanaged TaskOwnership = "unmanaged"
)

type ClassificationFact struct {
	Managed          bool
	Phase            jobs.JobPhase
	Intent           jobs.ActivityIntent
	ProblemCode      string
	NativeStatus     string
	NativeSeeder     bool
	NativeMetadata   bool
	IdentityConflict bool
}

type TaskClassification struct {
	Status    TaskStatus
	Ownership TaskOwnership
	Phase     string
	Actions   []TaskAction
}

func ClassifyTask(fact ClassificationFact) TaskClassification {
	result := TaskClassification{Ownership: OwnershipUnmanaged}
	if fact.Managed {
		result.Ownership = OwnershipManaged
		result.Phase = string(fact.Phase)
	}
	switch {
	case fact.Managed && fact.IdentityConflict:
		result.Status = StatusError
	case fact.Managed && fact.Phase == jobs.PhaseRemoved && fact.ProblemCode != "":
		result.Status = StatusError
	case fact.Managed && fact.Phase == jobs.PhaseRemoved:
		result.Status = StatusRemoved
	case fact.Managed && fact.ProblemCode != "" && fact.ProblemCode != "PowerLossDurabilityUnavailable":
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
	case fact.NativeStatus == "complete":
		result.Status = StatusComplete
	case fact.NativeStatus == "error":
		result.Status = StatusError
	case fact.NativeStatus == "removed":
		result.Status = StatusRemoved
	case fact.Managed && fact.Intent == jobs.ActivityStopped && fact.Phase != jobs.PhasePublished:
		result.Status = StatusPaused
	case fact.Managed && fact.Intent == jobs.ActivityStopped && fact.Phase == jobs.PhasePublished:
		result.Status = StatusComplete
	case fact.Managed && fact.Phase == jobs.PhasePublishing:
		result.Status = StatusDownloading
	default:
		result.Status = StatusError
	}
	return result
}
