package app

import "github.com/amio/aria2s/internal/jobs"

type TaskStatus string
type TaskOwnership string
type TaskAction string

const (
	StatusDownloading  TaskStatus    = "downloading"
	StatusSeeding      TaskStatus    = "seeding"
	StatusQueued       TaskStatus    = "queued"
	StatusPaused       TaskStatus    = "paused"
	StatusFinished     TaskStatus    = "finished"
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
	NativeWaiting    bool
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
	case fact.NativeStatus == "active" && fact.NativeSeeder:
		result.Status = StatusSeeding
	case fact.NativeStatus == "active":
		result.Status = StatusDownloading
	case fact.NativeStatus == "waiting" || (fact.NativeWaiting && fact.NativeStatus != "paused"):
		result.Status = StatusQueued
	case fact.Managed && fact.Intent == jobs.ActivityStopped && fact.Phase != jobs.PhasePublished:
		result.Status = StatusPaused
	case fact.Managed && fact.Intent == jobs.ActivityStopped && fact.Phase == jobs.PhasePublished:
		result.Status = StatusFinished
	case fact.NativeStatus == "error":
		result.Status = StatusError
	case fact.Managed && fact.Phase == jobs.PhasePublishing:
		result.Status = StatusDownloading
	case fact.NativeStatus == "paused":
		result.Status = StatusPaused
	case fact.NativeStatus == "complete":
		result.Status = StatusFinished
	case fact.NativeStatus == "removed":
		result.Status = StatusRemoved
	default:
		result.Status = StatusError
	}
	return result
}

type StatusCounts struct {
	Visible, Downloading, Seeding, Queued, Paused, Finished, Error, Removed int
}

func CountStatuses(rows []TaskClassification) StatusCounts {
	counts := StatusCounts{Visible: len(rows)}
	for _, row := range rows {
		switch row.Status {
		case StatusDownloading:
			counts.Downloading++
		case StatusSeeding:
			counts.Seeding++
		case StatusQueued:
			counts.Queued++
		case StatusPaused:
			counts.Paused++
		case StatusFinished:
			counts.Finished++
		case StatusError:
			counts.Error++
		case StatusRemoved:
			counts.Removed++
		}
	}
	return counts
}
