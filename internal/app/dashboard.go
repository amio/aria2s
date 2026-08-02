package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/jobs"
	"github.com/amio/aria2s/internal/state"
)

/** AddResult separates confirmed remote success from local metadata warnings. */
type AddResult struct {
	GID     string
	Warning error
}

/** RetryResult preserves a confirmed replacement when old-result cleanup fails. */
type RetryResult struct {
	NewGID         string
	CleanupWarning error
}

/** DashboardSession binds immutable RPC identity for one dashboard program lifetime. */
type DashboardSession struct {
	app      *App
	identity state.State
	rpc      dashboardRPC
}

func (session *DashboardSession) Snapshot(ctx context.Context, query aria2.DashboardQuery) (aria2.DashboardRead, error) {
	ctx, cancel := context.WithTimeout(ctx, session.app.options.DashboardReadTimeout)
	defer cancel()
	scanned, scanErr := jobs.New(session.app.options.Paths.StateDir).Scan()
	if scanErr != nil {
		return aria2.DashboardRead{}, scanErr
	}
	requestedJobID := query.DetailGID
	for _, item := range scanned {
		if item.Err != nil {
			continue
		}
		if item.Job.Execution != nil {
			query.ManagedGIDs = append(query.ManagedGIDs, item.Job.Execution.GID)
		}
		if requestedJobID == item.ID {
			if item.Job.Execution != nil {
				query.DetailGID = item.Job.Execution.GID
			} else {
				query.DetailGID = ""
			}
		}
	}
	read, err := session.rpc.DashboardSnapshot(ctx, session.identity, query)
	if err != nil {
		return read, err
	}
	// ListErr stays nested so the TUI can retain its last complete list. The
	// independently valid detail result must still receive app-owned status
	// classification before it is applied.
	return session.decorateSnapshot(read, scanned, query, requestedJobID), nil
}

func (session *DashboardSession) decorateSnapshot(read aria2.DashboardRead, scanned []jobs.ScannedJob, query aria2.DashboardQuery, requestedJobID ...string) aria2.DashboardRead {
	managed := make(map[string]jobs.Job)
	managedByExecution := make(map[string]jobs.Job)
	for _, item := range scanned {
		if item.Err == nil {
			managed[item.ID] = item.Job
			if item.Job.Execution != nil {
				managedByExecution[item.Job.Execution.GID] = item.Job
			}
		} else {
			read.Downloads.Stopped = append(read.Downloads.Stopped, aria2.Download{GID: item.ID, Status: "error", Name: "corrupt managed manifest", CanonicalStatus: string(StatusError), Ownership: string(OwnershipManaged), IssueCode: "CorruptManifest", IssueText: issueText("CorruptManifest"), Actions: []string{"clear"}})
		}
	}
	seen := make(map[string]struct{})
	windowed := make(map[string]struct{})
	for _, row := range append(append(append([]aria2.Download{}, read.Downloads.Active...), read.Downloads.Waiting...), read.Downloads.Stopped...) {
		windowed[row.GID] = struct{}{}
	}
	for gid, row := range read.Managed {
		if row == nil {
			continue
		}
		if _, ok := windowed[gid]; ok {
			continue
		}
		switch row.Status {
		case "active":
			read.Downloads.Active = append(read.Downloads.Active, *row)
		case "waiting", "paused":
			read.Downloads.Waiting = append(read.Downloads.Waiting, *row)
		default:
			read.Downloads.Stopped = append(read.Downloads.Stopped, *row)
		}
	}
	decorate := func(rows []aria2.Download) {
		for index := range rows {
			row := &rows[index]
			job, owned := managedByExecution[row.GID]
			fact := ClassificationFact{Managed: owned, NativeStatus: row.Status, NativeSeeder: row.Seeder, NativeMetadata: row.IsMetadata}
			if owned {
				fact.Lifecycle, fact.Intent, fact.IssueCode = derivedLifecycle(job), job.ActivityIntent, issueCodeForJob(job)
				fact.IdentityConflict = session.nativeDirConflict(job, row.Dir)
				row.AddedAt = job.CreatedAt
				row.GID = job.ID
				seen[job.ID] = struct{}{}
			}
			classification := ClassifyTask(fact)
			row.CanonicalStatus, row.Ownership = string(classification.Status), string(classification.Ownership)
			row.IssueCode = issueCodeForJob(job)
			row.IssueText = issueText(row.IssueCode)
			row.Actions = session.availableActions(classification, owned, job)
		}
	}
	decorate(read.Downloads.Active)
	decorate(read.Downloads.Waiting)
	decorate(read.Downloads.Stopped)
	if read.Detail != nil {
		job, owned := managedByExecution[read.Detail.GID]
		if owned {
			read.Detail.GID = job.ID
		}
		session.decorateDetail(read.Detail, job, owned)
	}
	for gid, job := range managed {
		if _, ok := seen[gid]; ok {
			continue
		}
		classification := ClassifyTask(ClassificationFact{Managed: true, Lifecycle: derivedLifecycle(job), Intent: job.ActivityIntent, IssueCode: issueCodeForJob(job), NativeAbsent: true})
		code := projectedIssueCode(job, true)
		row := aria2.Download{GID: gid, Status: "absent", Dir: job.TargetDir, Name: firstNonempty(job.FinalRoot(), job.Source), AddedAt: job.CreatedAt, CanonicalStatus: string(classification.Status), Ownership: string(classification.Ownership), IssueCode: code, IssueText: issueText(code), Actions: session.availableActions(classification, true, job)}
		applyPublishedMetrics(&row.CompletedLength, &row.TotalLength, &row.LengthKnown, job)
		read.Downloads.Stopped = append(read.Downloads.Stopped, row)
	}
	detailJobID := query.DetailGID
	if len(requestedJobID) > 0 && requestedJobID[0] != "" {
		detailJobID = requestedJobID[0]
	}
	if detailJobID != "" && read.Detail == nil {
		managedRow, absenceKnown := read.Managed[query.DetailGID]
		job, ok := managed[detailJobID]
		if ok && (job.Execution == nil || (aria2.IsNotFound(read.DetailErr) && absenceKnown && managedRow == nil)) {
			detail := session.manifestDetail(job)
			read.Detail = &detail
			read.DetailErr = nil
			read.DetailSourceErr = nil
		}
	}
	read.Downloads.Stopped = pageManagedHistory(read.Downloads.Stopped, managed, query.List)
	return read
}

func applyPublishedMetrics(completed, total *int64, known *bool, job jobs.Job) {
	length := job.Payload.Length
	if derivedLifecycle(job) != LifecyclePublished || length == nil {
		return
	}
	*completed = *length
	*total = *length
	*known = true
}

// manifestDetail supplies the stable local projection after aria2 has detached
// a managed task. It keeps detail-only commands, such as opening the payload,
// consistent with the Dashboard snapshot.
func (session *DashboardSession) manifestDetail(job jobs.Job) aria2.DownloadDetail {
	classification := ClassifyTask(ClassificationFact{
		Managed:      true,
		Lifecycle:    derivedLifecycle(job),
		Intent:       job.ActivityIntent,
		IssueCode:    issueCodeForJob(job),
		NativeAbsent: true,
	})
	detail := aria2.DownloadDetail{
		GID:             job.ID,
		Status:          "absent",
		Name:            firstNonempty(job.FinalRoot(), job.Source),
		PrimaryURI:      job.Source,
		DownloadDir:     job.TargetDir,
		CanonicalStatus: string(classification.Status),
		Ownership:       string(classification.Ownership),
		IssueCode:       projectedIssueCode(job, true),
		Actions:         session.availableActions(classification, true, job),
	}
	detail.IssueText = issueText(detail.IssueCode)
	applyPublishedMetrics(&detail.CompletedLength, &detail.TotalLength, &detail.LengthKnown, job)
	return detail
}

func (session *DashboardSession) decorateDetail(detail *aria2.DownloadDetail, job jobs.Job, managed bool) {
	fact := ClassificationFact{
		Managed:        managed,
		NativeStatus:   detail.Status,
		NativeSeeder:   detail.Seeder,
		NativeMetadata: detail.IsMetadata,
	}
	if managed {
		fact.Lifecycle = derivedLifecycle(job)
		fact.Intent = job.ActivityIntent
		fact.IssueCode = issueCodeForJob(job)
		fact.IdentityConflict = session.nativeDirConflict(job, detail.DownloadDir)
	}
	classification := ClassifyTask(fact)
	detail.CanonicalStatus = string(classification.Status)
	detail.Ownership = string(classification.Ownership)
	detail.IssueCode = issueCodeForJob(job)
	detail.IssueText = issueText(detail.IssueCode)
	detail.Actions = session.availableActions(classification, managed, job)
}

func projectedIssueCode(job jobs.Job, nativeAbsent bool) string {
	if code := issueCodeForJob(job); code != "" {
		return code
	}
	if nativeAbsent && derivedLifecycle(job) == LifecyclePublishing {
		return "PublicationRecoveryRequired"
	}
	return ""
}

func pageManagedHistory(rows []aria2.Download, managed map[string]jobs.Job, page aria2.ListQuery) []aria2.Download {
	var native, history []aria2.Download
	for _, row := range rows {
		if row.Ownership == string(OwnershipManaged) {
			history = append(history, row)
		} else {
			native = append(native, row)
		}
	}
	sort.SliceStable(history, func(left, right int) bool {
		leftJob, leftOK := managed[history[left].GID]
		rightJob, rightOK := managed[history[right].GID]
		if leftOK != rightOK {
			return leftOK
		}
		if leftJob.UpdatedAt.Equal(rightJob.UpdatedAt) {
			return history[left].GID < history[right].GID
		}
		return leftJob.UpdatedAt.After(rightJob.UpdatedAt)
	})
	start := min(max(page.StoppedOffset, 0), len(history))
	limit := page.StoppedLimit
	if limit <= 0 {
		limit = 100
	}
	end := min(start+limit, len(history))
	return append(native, history[start:end]...)
}

func (session *DashboardSession) availableActions(classification TaskClassification, managed bool, job jobs.Job) []string {
	if managed && job.Issue != nil {
		if metadata, ok := jobs.LookupIssue(job.Issue.Code); ok && len(metadata.Actions) > 0 {
			return metadata.Actions
		}
	}
	if managed && derivedLifecycle(job) == LifecyclePublishing {
		if classification.Status != StatusError {
			return nil
		}
		return []string{"retry"}
	}
	var actions []string
	switch classification.Status {
	case StatusDownloading, StatusSeeding, StatusMetadata:
		actions = append(actions, "pause", "remove")
	case StatusWaiting:
		actions = append(actions, "pause", "remove")
	case StatusPaused:
		actions = append(actions, "resume", "remove")
	case StatusError:
		actions = append(actions, "retry")
		if !managed || (derivedLifecycle(job) != LifecyclePublishing && !job.Removed) {
			actions = append(actions, "remove")
		}
	case StatusComplete:
		if managed && !job.PublicationRenamed() && session.hasMetainfo(job.ID) {
			actions = append(actions, "start-seeding")
		}
		actions = append(actions, "clear")
	case StatusRemoved:
		actions = append(actions, "retry", "clear")
	}
	return actions
}

func (session *DashboardSession) hasMetainfo(gid string) bool {
	info, err := os.Stat(jobs.New(session.app.options.Paths.StateDir).MetainfoPath(gid))
	return err == nil && info.Mode().IsRegular()
}

func (session *DashboardSession) nativeDirConflict(job jobs.Job, nativeDir string) bool {
	if nativeDir == "" || job.Removed {
		return false
	}
	expected := job.TargetDir
	if derivedLifecycle(job) != LifecyclePublished {
		scope, err := jobs.New(session.app.options.Paths.StateDir).LoadStorage(job.StorageID)
		if err != nil {
			return true
		}
		expected = jobs.WorkDir(scope, job.ID)
	}
	return filepath.Clean(nativeDir) != filepath.Clean(expected)
}

func issueCodeForJob(job jobs.Job) string {
	if job.Issue != nil {
		return job.Issue.Code
	}
	return ""
}

func derivedLifecycle(job jobs.Job) ManagedLifecycle {
	switch {
	case job.Removed:
		return LifecycleRemoved
	case job.Payload.Location == jobs.PayloadPublished:
		return LifecyclePublished
	case job.Payload.FinalRoot != "":
		return LifecyclePublishing
	case job.Execution == nil:
		return LifecyclePending
	default:
		return LifecycleStaged
	}
}

func issueText(code string) string {
	if metadata, ok := jobs.LookupIssue(code); ok {
		return metadata.Text
	}
	return ""
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "managed task"
}

func (session *DashboardSession) TaskDetail(ctx context.Context, gid string) (aria2.DownloadDetail, error) {
	ctx, cancel := context.WithTimeout(ctx, session.app.options.DashboardReadTimeout)
	defer cancel()
	job, _, loadErr := jobs.New(session.app.options.Paths.StateDir).Load(gid)
	nativeGID := gid
	if loadErr == nil && job.Execution != nil {
		nativeGID = job.Execution.GID
	}
	if loadErr == nil && job.Execution == nil {
		return session.manifestDetail(job), nil
	}
	detail, err := session.rpc.TaskDetail(ctx, session.identity, nativeGID)
	if err != nil {
		if loadErr == nil && aria2.IsNotFound(err) {
			return session.manifestDetail(job), nil
		}
		return detail, err
	}
	if loadErr == nil {
		detail.GID = job.ID
		session.decorateDetail(&detail, job, true)
	} else {
		session.decorateDetail(&detail, jobs.Job{}, false)
	}
	return detail, nil
}

func (session *DashboardSession) AddURI(ctx context.Context, uri string, options aria2.AddOptions) (AddResult, error) {
	ctx, cancel := context.WithTimeout(ctx, session.app.options.DashboardMutationTimeout)
	defer cancel()
	result, err := session.app.AddManaged(ctx, AddRequest{Source: uri, TargetDir: options.Dir})
	if err != nil {
		return AddResult{}, err
	}
	return AddResult{GID: result.Task.JobID, Warning: result.Warning}, nil
}

func (session *DashboardSession) RecentDirs(ctx context.Context) ([]string, error) {
	return session.app.RecentDirs(ctx)
}

func (session *DashboardSession) DefaultDir() string { return session.app.DefaultDir() }

func (session *DashboardSession) Pause(ctx context.Context, gid string) error {
	if session.managed(gid) {
		return session.app.SetActivity(ctx, gid, false)
	}
	return session.mutate(ctx, func(ctx context.Context) error { return session.rpc.Pause(ctx, session.identity, gid) })
}

func (session *DashboardSession) Resume(ctx context.Context, gid string) error {
	if session.managed(gid) {
		return session.app.SetActivity(ctx, gid, true)
	}
	return session.mutate(ctx, func(ctx context.Context) error { return session.rpc.Resume(ctx, session.identity, gid) })
}

func (session *DashboardSession) Remove(ctx context.Context, gid string) error {
	if session.managed(gid) {
		return session.app.RemoveManaged(ctx, gid)
	}
	return session.mutate(ctx, func(ctx context.Context) error { return session.rpc.Remove(ctx, session.identity, gid) })
}

func (session *DashboardSession) managed(gid string) bool {
	repository := jobs.New(session.app.options.Paths.StateDir)
	return repository.Exists(gid)
}

func (session *DashboardSession) ClearStopped(ctx context.Context, gid string) error {
	if session.managed(gid) {
		return session.app.ClearManaged(ctx, gid)
	}
	return session.mutate(ctx, func(ctx context.Context) error { return session.rpc.ClearStopped(ctx, session.identity, gid) })
}

func (session *DashboardSession) mutate(ctx context.Context, operation func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, session.app.options.DashboardMutationTimeout)
	defer cancel()
	return operation(ctx)
}

func (session *DashboardSession) Retry(ctx context.Context, gid string) (RetryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, session.app.options.DashboardMutationTimeout)
	defer cancel()
	if session.managed(gid) {
		if err := session.app.RetryManaged(ctx, gid); err != nil {
			return RetryResult{}, err
		}
		return RetryResult{NewGID: gid}, nil
	}
	source, err := session.rpc.RetrySource(ctx, session.identity, gid)
	if err != nil {
		return RetryResult{}, fmt.Errorf("read retry source: %w", err)
	}
	newGID, err := session.rpc.AddURIs(ctx, session.identity, source.URIs, aria2.AddOptions{Dir: source.Dir})
	if err != nil {
		return RetryResult{}, fmt.Errorf("add retry replacement: %w", err)
	}
	// The replacement must be confirmed before cleanup: an unknown Add outcome must
	// never destroy the only authoritative row or trigger an automatic duplicate.
	result := RetryResult{NewGID: newGID}
	if err := session.rpc.ClearStopped(ctx, session.identity, gid); err != nil {
		result.CleanupWarning = fmt.Errorf("clear old stopped result: %w", err)
	}
	return result, nil
}
