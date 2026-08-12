// Package app owns dashboard task-state projection. Public states reuse aria2
// names except for the user-relevant downloading, metadata, and seeding
// substates of native active work; managed phases remain internal facts.
package app

import (
	"time"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/jobs"
)

// DashboardListWindow bounds the waiting and stopped task windows requested by
// the product UI. It is translated to native RPC offsets only at the app edge.
type DashboardListWindow struct {
	WaitingLimit  int
	StoppedOffset int
	StoppedLimit  int
}

type DashboardQuery struct {
	List                DashboardListWindow
	DetailGID           string
	ResolveDetailSource bool
}

// DashboardRead preserves independent validity for list, detail, and detail
// source results while exposing only stable product identities.
type DashboardRead struct {
	Downloads       TaskSnapshot
	ListErr         error
	Detail          *TaskDetail
	DetailErr       error
	DetailSourceErr error
}

type TaskSnapshot struct {
	Active  []TaskRow
	Waiting []TaskRow
	Stopped []TaskRow
}

type TaskRow struct {
	GID               string
	Status            string
	Dir               string
	Name              string
	IsMetadata        bool
	CompletedLength   int64
	TotalLength       int64
	LengthKnown       bool
	DownloadSpeed     int64
	UploadSpeed       int64
	UploadLength      int64
	UploadLengthKnown bool
	NumSeeders        int64
	Connections       int64
	Seeder            bool
	InfoHash          string
	AddedAt           time.Time
	CanonicalStatus   string
	Ownership         string
	IssueCode         string
	IssueText         string
	Actions           []string
}

type TaskDetail struct {
	GID                    string
	Status                 string
	Name                   string
	IsMetadata             bool
	CompletedLength        int64
	TotalLength            int64
	LengthKnown            bool
	DownloadSpeed          int64
	UploadSpeed            int64
	UploadLength           int64
	VerifiedLength         int64
	VerifyIntegrityPending bool
	InfoHash               string
	NumSeeders             int64
	Seeder                 bool
	PieceLength            int64
	NumPieces              int64
	PrimaryURI             string
	TargetDir              string
	DownloadDir            string
	Connections            int64
	ErrorCode              string
	ErrorMessage           string
	Files                  []TaskFile
	CanonicalStatus        string
	Ownership              string
	IssueCode              string
	IssueText              string
	Actions                []string
}

type TaskFile struct {
	Path            string
	Name            string
	Length          int64
	CompletedLength int64
	Selected        bool
}

func taskRowFromNative(row aria2.Download) TaskRow {
	return TaskRow{
		GID: row.GID, Status: row.Status, Dir: row.Dir, Name: row.Name,
		IsMetadata: row.IsMetadata, CompletedLength: row.CompletedLength,
		TotalLength: row.TotalLength, LengthKnown: row.LengthKnown,
		DownloadSpeed: row.DownloadSpeed, UploadSpeed: row.UploadSpeed,
		UploadLength: row.UploadLength, UploadLengthKnown: row.UploadLengthKnown,
		NumSeeders: row.NumSeeders, Connections: row.Connections, Seeder: row.Seeder,
		InfoHash: row.InfoHash, AddedAt: row.AddedAt,
	}
}

func taskRowsFromNative(rows []aria2.Download) []TaskRow {
	if rows == nil {
		return nil
	}
	result := make([]TaskRow, len(rows))
	for index, row := range rows {
		result[index] = taskRowFromNative(row)
	}
	return result
}

func taskSnapshotFromNative(snapshot aria2.DownloadSnapshot) TaskSnapshot {
	return TaskSnapshot{
		Active: taskRowsFromNative(snapshot.Active), Waiting: taskRowsFromNative(snapshot.Waiting),
		Stopped: taskRowsFromNative(snapshot.Stopped),
	}
}

func taskDetailFromNative(detail aria2.DownloadDetail) TaskDetail {
	var files []TaskFile
	if detail.Files != nil {
		files = make([]TaskFile, len(detail.Files))
		for index, file := range detail.Files {
			files[index] = TaskFile(file)
		}
	}
	return TaskDetail{
		GID: detail.GID, Status: detail.Status, Name: detail.Name, IsMetadata: detail.IsMetadata,
		CompletedLength: detail.CompletedLength, TotalLength: detail.TotalLength, LengthKnown: detail.LengthKnown,
		DownloadSpeed: detail.DownloadSpeed, UploadSpeed: detail.UploadSpeed, UploadLength: detail.UploadLength,
		VerifiedLength: detail.VerifiedLength, VerifyIntegrityPending: detail.VerifyIntegrityPending,
		InfoHash: detail.InfoHash, NumSeeders: detail.NumSeeders, Seeder: detail.Seeder,
		PieceLength: detail.PieceLength, NumPieces: detail.NumPieces, PrimaryURI: detail.PrimaryURI,
		DownloadDir: detail.DownloadDir, Connections: detail.Connections, ErrorCode: detail.ErrorCode,
		ErrorMessage: detail.ErrorMessage, Files: files,
	}
}

type TaskStatus string
type TaskOwnership string
type ManagedLifecycle string

const (
	StatusDownloading   TaskStatus       = "downloading"
	StatusSeeding       TaskStatus       = "seeding"
	StatusMetadata      TaskStatus       = "metadata"
	StatusWaiting       TaskStatus       = "waiting"
	StatusPaused        TaskStatus       = "paused"
	StatusComplete      TaskStatus       = "complete"
	StatusError         TaskStatus       = "error"
	OwnershipManaged    TaskOwnership    = "managed"
	OwnershipUnmanaged  TaskOwnership    = "unmanaged"
	LifecyclePending    ManagedLifecycle = "pending"
	LifecycleStaged     ManagedLifecycle = "staged"
	LifecyclePublishing ManagedLifecycle = "publishing"
	LifecyclePublished  ManagedLifecycle = "published"
	LifecycleRemoved    ManagedLifecycle = "removed"
)

// TaskFacts contains every observation needed to project one Dashboard task.
// Callers collect filesystem and native facts before entering this pure policy.
type TaskFacts struct {
	Managed          bool
	Lifecycle        ManagedLifecycle
	Intent           jobs.ActivityIntent
	IssueCode        string
	NativeStatus     string
	NativeSeeder     bool
	NativeMetadata   bool
	NativeAbsent     bool
	IdentityConflict bool
	CanStartSeeding  bool
}

type TaskProjection struct {
	Status    TaskStatus
	Ownership TaskOwnership
	IssueCode string
	IssueText string
	Actions   []string
}

// ProjectTask is the sole owner of Dashboard status, ownership, issue, and
// action policy. It does not observe the filesystem or mutate lifecycle state.
func ProjectTask(fact TaskFacts) TaskProjection {
	result := TaskProjection{Ownership: OwnershipUnmanaged}
	issueCode := fact.IssueCode
	if issueCode == "" && fact.Managed && fact.NativeAbsent && fact.Lifecycle == LifecyclePublishing {
		issueCode = "PublicationRecoveryRequired"
	}
	issueMetadata, issueKnown := jobs.LookupIssue(issueCode)
	issueIsError := issueCode != "" && (!issueKnown || issueMetadata.Severity == "error")
	if fact.Managed {
		result.Ownership = OwnershipManaged
	}
	switch {
	case fact.Managed && fact.Lifecycle == LifecycleRemoved:
		result.Status = StatusError
	case fact.Managed && fact.IdentityConflict:
		result.Status = StatusError
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
		result.Status = StatusError
	case fact.Managed && fact.Intent == jobs.ActivityStopped && fact.Lifecycle != LifecyclePublished:
		result.Status = StatusPaused
	case fact.Managed && fact.Intent == jobs.ActivityStopped && fact.Lifecycle == LifecyclePublished:
		result.Status = StatusComplete
	case fact.Managed && fact.Lifecycle == LifecyclePublishing:
		result.Status = StatusDownloading
	default:
		result.Status = StatusError
	}

	result.IssueCode = issueCode
	if issueKnown {
		result.IssueText = issueMetadata.Text
	}
	if (fact.Managed && fact.Lifecycle == LifecycleRemoved) || fact.NativeStatus == "removed" {
		result.Actions = []string{"remove"}
		return result
	}
	if issueKnown {
		// nil means this issue does not override ordinary status capabilities;
		// a non-nil empty slice explicitly suppresses every action.
		if issueMetadata.Actions != nil {
			result.Actions = issueMetadata.Actions
			if len(result.Actions) > 0 {
				result.Actions = appendAction(result.Actions, "remove")
			}
			return result
		}
	}

	if fact.Managed && fact.Lifecycle == LifecyclePublishing {
		if result.Status == StatusError {
			result.Actions = []string{"retry"}
		}
		result.Actions = appendAction(result.Actions, "remove")
		return result
	}
	switch result.Status {
	case StatusDownloading, StatusSeeding:
		result.Actions = []string{"pause", "remove"}
	case StatusMetadata:
		result.Actions = []string{"pause", "remove"}
	case StatusWaiting:
		result.Actions = []string{"pause", "remove"}
	case StatusPaused:
		result.Actions = []string{"resume", "remove"}
	case StatusError:
		result.Actions = []string{"retry", "remove"}
	case StatusComplete:
		if fact.Managed && fact.CanStartSeeding {
			result.Actions = append(result.Actions, "reseed")
		}
		result.Actions = append(result.Actions, "remove")
	}
	return result
}

func appendAction(actions []string, action string) []string {
	for _, candidate := range actions {
		if candidate == action {
			return actions
		}
	}
	return append(actions, action)
}

func applyTaskRowProjection(row *TaskRow, projection TaskProjection) {
	row.CanonicalStatus = string(projection.Status)
	row.Ownership = string(projection.Ownership)
	row.IssueCode = projection.IssueCode
	row.IssueText = projection.IssueText
	row.Actions = cloneActions(projection.Actions)
}

func applyTaskDetailProjection(detail *TaskDetail, projection TaskProjection) {
	detail.CanonicalStatus = string(projection.Status)
	detail.Ownership = string(projection.Ownership)
	detail.IssueCode = projection.IssueCode
	detail.IssueText = projection.IssueText
	detail.Actions = cloneActions(projection.Actions)
}

func cloneActions(actions []string) []string {
	if actions == nil {
		return nil
	}
	return append([]string{}, actions...)
}
