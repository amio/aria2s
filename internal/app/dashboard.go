package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

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

/** DashboardSession binds RPC identity and memoizes readiness for one dashboard program lifetime. */
type DashboardSession struct {
	app      *App
	identity state.State
	rpc      dashboardRPC
	ready    atomic.Bool
}

// StartupStatus returns only the disposable presentation hint published by
// managed-exec. RPC success remains the authoritative readiness signal.
func (session *DashboardSession) StartupStatus() string {
	progress, err := readStartupProgress(session.app.options.Paths.StartupProgressFile)
	if err != nil {
		return ""
	}
	return progress.message()
}

func (session *DashboardSession) startupError(err error) error {
	if progress, progressErr := readStartupProgress(session.app.options.Paths.StartupProgressFile); progressErr == nil {
		return &dashboardStartupError{cause: err, progress: progress}
	}
	return err
}

func (session *DashboardSession) Snapshot(ctx context.Context, query DashboardQuery) (DashboardRead, error) {
	ctx, cancel := context.WithTimeout(ctx, session.app.options.DashboardReadTimeout)
	defer cancel()
	scanned, scanErr := jobs.New(session.app.options.Paths.StateDir).Scan()
	if scanErr != nil {
		return DashboardRead{}, scanErr
	}
	requestedJobID := query.DetailGID
	nativeQuery := aria2.ReadBatchQuery{
		List: aria2.ListOptions{
			WaitingLimit: query.List.WaitingLimit, StoppedOffset: query.List.StoppedOffset,
			StoppedLimit: query.List.StoppedLimit,
		},
		DetailGID: query.DetailGID, ResolveDetailSource: query.ResolveDetailSource,
	}
	for _, item := range scanned {
		if item.Err != nil {
			continue
		}
		if item.Job.Execution != nil {
			nativeQuery.ObserveGIDs = append(nativeQuery.ObserveGIDs, item.Job.Execution.GID)
		}
		if requestedJobID == item.ID {
			if item.Job.Execution != nil {
				nativeQuery.DetailGID = item.Job.Execution.GID
			} else {
				nativeQuery.DetailGID = ""
			}
		}
	}
	if !session.ready.Load() {
		if _, err := session.rpc.Version(ctx, session.identity); err != nil {
			return DashboardRead{}, session.startupError(err)
		}
		session.ready.Store(true)
	}
	read, err := session.rpc.ReadBatch(ctx, session.identity, nativeQuery)
	if err != nil {
		return DashboardRead{}, session.startupError(err)
	}
	_ = clearStartupProgress(session.app.options.Paths.StartupProgressFile)
	// ListErr stays nested so the TUI can retain its last complete list. The
	// independently valid detail result must still receive app-owned status
	// classification before it is applied.
	return session.projectSnapshot(read, scanned, nativeQuery, requestedJobID), nil
}

func (session *DashboardSession) projectSnapshot(native aria2.ReadBatch, scanned []jobs.ScannedJob, query aria2.ReadBatchQuery, requestedJobID string) DashboardRead {
	read := DashboardRead{
		Downloads: taskSnapshotFromNative(native.Downloads), ListErr: native.ListErr,
		DetailErr: native.DetailErr, DetailSourceErr: native.DetailSourceErr,
	}
	if native.Detail != nil {
		detail := taskDetailFromNative(*native.Detail)
		read.Detail = &detail
	}
	managed := make(map[string]jobs.Job)
	managedByExecution := make(map[string]jobs.Job)
	var corruptRows []TaskRow
	for _, item := range scanned {
		if item.Err == nil {
			managed[item.ID] = item.Job
			if item.Job.Execution != nil {
				managedByExecution[item.Job.Execution.GID] = item.Job
			}
		} else {
			row := TaskRow{GID: item.ID, Status: "error", Name: "corrupt managed manifest"}
			applyTaskRowProjection(&row, ProjectTask(TaskFacts{Managed: true, NativeAbsent: true, IssueCode: "CorruptManifest"}))
			corruptRows = append(corruptRows, row)
		}
	}
	seen := make(map[string]struct{})
	windowed := make(map[string]struct{})
	for _, row := range append(append(append([]TaskRow{}, read.Downloads.Active...), read.Downloads.Waiting...), read.Downloads.Stopped...) {
		windowed[row.GID] = struct{}{}
	}
	for gid, row := range native.Observed {
		if row == nil {
			continue
		}
		if _, ok := windowed[gid]; ok {
			continue
		}
		switch row.Status {
		case "active":
			read.Downloads.Active = append(read.Downloads.Active, taskRowFromNative(*row))
		case "waiting", "paused":
			read.Downloads.Waiting = append(read.Downloads.Waiting, taskRowFromNative(*row))
		default:
			read.Downloads.Stopped = append(read.Downloads.Stopped, taskRowFromNative(*row))
		}
	}
	decorate := func(rows []TaskRow) {
		for index := range rows {
			row := &rows[index]
			job, owned := managedByExecution[row.GID]
			if owned {
				row.AddedAt = job.CreatedAt
				row.GID = job.ID
				seen[job.ID] = struct{}{}
			}
			applyTaskRowProjection(row, session.projectTask(job, owned, taskObservation{
				status: row.Status, seeder: row.Seeder, metadata: row.IsMetadata, dir: row.Dir,
			}))
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
		row := TaskRow{GID: gid, Status: "absent", Dir: job.TargetDir, Name: manifestDisplayName(job), AddedAt: job.CreatedAt}
		applyTaskRowProjection(&row, session.projectTask(job, true, taskObservation{absent: true}))
		applyPublishedMetrics(&row.CompletedLength, &row.TotalLength, &row.LengthKnown, job)
		read.Downloads.Stopped = append(read.Downloads.Stopped, row)
	}
	read.Downloads.Stopped = append(read.Downloads.Stopped, corruptRows...)
	detailJobID := query.DetailGID
	if requestedJobID != "" {
		detailJobID = requestedJobID
	}
	if detailJobID != "" && read.Detail == nil {
		managedRow, absenceKnown := native.Observed[query.DetailGID]
		job, ok := managed[detailJobID]
		if ok && (job.Execution == nil || (aria2.IsNotFound(read.DetailErr) && absenceKnown && managedRow == nil)) {
			detail := session.manifestDetail(job)
			read.Detail = &detail
			read.DetailErr = nil
			read.DetailSourceErr = nil
		}
	}
	read.Downloads.Stopped = pageManagedHistory(read.Downloads.Stopped, managed, DashboardListWindow{
		WaitingLimit: query.List.WaitingLimit, StoppedOffset: query.List.StoppedOffset, StoppedLimit: query.List.StoppedLimit,
	})
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
func (session *DashboardSession) manifestDetail(job jobs.Job) TaskDetail {
	detail := TaskDetail{
		GID:         job.ID,
		Status:      "absent",
		Name:        manifestDisplayName(job),
		PrimaryURI:  job.Source,
		TargetDir:   job.TargetDir,
		DownloadDir: job.TargetDir,
	}
	applyTaskDetailProjection(&detail, session.projectTask(job, true, taskObservation{absent: true}))
	applyPublishedMetrics(&detail.CompletedLength, &detail.TotalLength, &detail.LengthKnown, job)
	detail.Files = session.publishedManifestFiles(job)
	return detail
}

// publishedManifestFiles restores detail-only file facts after managed aria2
// state has been retired. Retained torrent metainfo owns multi-file layout;
// downloads without metainfo have the single published payload root.
func (session *DashboardSession) publishedManifestFiles(job jobs.Job) []TaskFile {
	if job.Payload.Location != jobs.PayloadPublished || job.FinalRoot() == "" {
		return nil
	}
	repository := jobs.New(session.app.options.Paths.StateDir)
	metainfo, err := repository.ReadMetainfo(job.ID)
	if err == nil {
		layout, layoutErr := aria2.MetainfoFileLayout(metainfo)
		if layoutErr != nil {
			return nil
		}
		return projectPublishedTorrentFiles(job, layout)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if job.Payload.Length == nil {
		return nil
	}
	length := *job.Payload.Length
	path := filepath.Join(job.TargetDir, job.FinalRoot())
	return []TaskFile{{Path: path, Name: filepath.Base(path), Length: length, CompletedLength: length, Selected: true}}
}

func projectPublishedTorrentFiles(job jobs.Job, layout aria2.MetainfoLayout) []TaskFile {
	root := filepath.Join(job.TargetDir, job.FinalRoot())
	files := make([]TaskFile, 0, len(layout.Files))
	for _, file := range layout.Files {
		path := root
		if layout.MultiFile {
			if !safeMetainfoPath(file.Path) {
				return nil
			}
			path = filepath.Join(append([]string{root}, file.Path...)...)
		}
		files = append(files, TaskFile{
			Path: path, Name: filepath.Base(path), Length: file.Length,
			CompletedLength: file.Length, Selected: true,
		})
	}
	return files
}

func safeMetainfoPath(path []string) bool {
	if len(path) == 0 {
		return false
	}
	for _, component := range path {
		if component == "" || component == "." || component == ".." ||
			filepath.IsAbs(component) || filepath.Base(component) != component ||
			strings.IndexByte(component, 0) >= 0 {
			return false
		}
	}
	return true
}

func manifestDisplayName(job jobs.Job) string {
	return firstNonempty(job.FinalRoot(), job.DisplayName, job.Source)
}

func (session *DashboardSession) decorateDetail(detail *TaskDetail, job jobs.Job, managed bool) {
	applyTaskDetailProjection(detail, session.projectTask(job, managed, taskObservation{
		status: detail.Status, seeder: detail.Seeder, metadata: detail.IsMetadata, dir: detail.DownloadDir,
	}))
	if managed {
		detail.TargetDir = job.TargetDir
	}
}

type taskObservation struct {
	status   string
	seeder   bool
	metadata bool
	dir      string
	absent   bool
}

func (session *DashboardSession) projectTask(job jobs.Job, managed bool, observation taskObservation) TaskProjection {
	facts := TaskFacts{
		Managed: managed, NativeStatus: observation.status, NativeSeeder: observation.seeder,
		NativeMetadata: observation.metadata, NativeAbsent: observation.absent,
	}
	if managed {
		facts.Lifecycle = derivedLifecycle(job)
		facts.Intent = job.ActivityIntent
		facts.IssueCode = issueCodeForJob(job)
		facts.IdentityConflict = !observation.absent && session.nativeDirConflict(job, observation.dir)
		facts.CanStartSeeding = session.canStartSeeding(job, observation)
	}
	return ProjectTask(facts)
}

func pageManagedHistory(rows []TaskRow, managed map[string]jobs.Job, page DashboardListWindow) []TaskRow {
	var native, history []TaskRow
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

func (session *DashboardSession) canStartSeeding(job jobs.Job, observation taskObservation) bool {
	mayComplete := (observation.status == "complete" && !observation.metadata) || (observation.absent && derivedLifecycle(job) == LifecyclePublished && job.ActivityIntent == jobs.ActivityStopped)
	return mayComplete && !job.PublicationRenamed() && session.hasMetainfo(job.ID)
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

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "managed task"
}

func (session *DashboardSession) TaskDetail(ctx context.Context, gid string) (TaskDetail, error) {
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
	nativeDetail, err := session.rpc.TaskDetail(ctx, session.identity, nativeGID)
	if err != nil {
		if loadErr == nil && aria2.IsNotFound(err) {
			return session.manifestDetail(job), nil
		}
		return taskDetailFromNative(nativeDetail), err
	}
	detail := taskDetailFromNative(nativeDetail)
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

func (session *DashboardSession) DeleteRecentDir(ctx context.Context, dir string) error {
	return session.app.DeleteRecentDir(ctx, dir)
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
	return session.mutate(ctx, func(ctx context.Context) error { return session.removeNativeTask(ctx, gid) })
}

func (session *DashboardSession) managed(gid string) bool {
	repository := jobs.New(session.app.options.Paths.StateDir)
	return repository.Exists(gid)
}

func (session *DashboardSession) removeNativeTask(ctx context.Context, gid string) error {
	detail, err := session.rpc.TaskDetail(ctx, session.identity, gid)
	if aria2.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var uncertainRemove error
	switch detail.Status {
	case "active", "waiting", "paused":
		if err := session.rpc.Remove(ctx, session.identity, gid); err != nil {
			if aria2.IsNotFound(err) {
				return nil
			}
			if !errors.Is(err, aria2.ErrOutcomeUnknown) {
				return err
			}
			uncertainRemove = err
		}
	case "complete", "error", "removed":
		return session.clearNativeTaskResult(ctx, gid)
	default:
		return fmt.Errorf("cannot remove native task in status %q", detail.Status)
	}

	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		detail, err = session.rpc.TaskDetail(ctx, session.identity, gid)
		if aria2.IsNotFound(err) {
			return nil
		}
		if err != nil {
			if uncertainRemove != nil {
				return errors.Join(uncertainRemove, err)
			}
			return err
		}
		if detail.Status == "complete" || detail.Status == "error" || detail.Status == "removed" {
			return session.clearNativeTaskResult(ctx, gid)
		}
		select {
		case <-ctx.Done():
			if uncertainRemove != nil {
				return uncertainRemove
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (session *DashboardSession) clearNativeTaskResult(ctx context.Context, gid string) error {
	err := session.rpc.ClearStopped(ctx, session.identity, gid)
	if err == nil || aria2.IsNotFound(err) {
		return nil
	}
	if !errors.Is(err, aria2.ErrOutcomeUnknown) {
		return err
	}
	_, observedErr := session.rpc.TaskDetail(ctx, session.identity, gid)
	if aria2.IsNotFound(observedErr) {
		return nil
	}
	return err
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
