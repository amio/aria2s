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
	for _, item := range scanned {
		if item.Err == nil {
			query.ManagedGIDs = append(query.ManagedGIDs, item.ID)
		}
	}
	read, err := session.rpc.DashboardSnapshot(ctx, session.identity, query)
	if err != nil {
		return read, err
	}
	// ListErr stays nested so the TUI can retain its last complete list. The
	// independently valid detail result must still receive app-owned status
	// classification before it is applied.
	return session.decorateSnapshot(read, scanned, query), nil
}

func (session *DashboardSession) decorateSnapshot(read aria2.DashboardRead, scanned []jobs.ScannedJob, query aria2.DashboardQuery) aria2.DashboardRead {
	managed := make(map[string]jobs.Job)
	for _, item := range scanned {
		if item.Err == nil {
			managed[item.ID] = item.Job
		} else {
			read.Downloads.Stopped = append(read.Downloads.Stopped, aria2.Download{GID: item.ID, Status: "error", Name: "corrupt managed manifest", CanonicalStatus: string(StatusError), Ownership: string(OwnershipManaged), Phase: "corrupt", ProblemCode: "CorruptManifest", Actions: []string{"clear"}})
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
			job, owned := managed[row.GID]
			fact := ClassificationFact{Managed: owned, NativeStatus: row.Status, NativeSeeder: row.Seeder, NativeMetadata: row.IsMetadata}
			if owned {
				fact.Phase, fact.Intent, fact.ProblemCode = job.Phase, job.ActivityIntent, job.ProblemCode
				fact.IdentityConflict = session.nativeDirConflict(job, row.Dir)
				seen[row.GID] = struct{}{}
			}
			classification := ClassifyTask(fact)
			row.CanonicalStatus, row.Ownership, row.Phase = string(classification.Status), string(classification.Ownership), classification.Phase
			row.ProblemCode = job.ProblemCode
			row.Actions = session.availableActions(classification, owned, job)
		}
	}
	decorate(read.Downloads.Active)
	decorate(read.Downloads.Waiting)
	decorate(read.Downloads.Stopped)
	if read.Detail != nil {
		job, owned := managed[read.Detail.GID]
		session.decorateDetail(read.Detail, job, owned)
	}
	for gid, job := range managed {
		if _, ok := seen[gid]; ok {
			continue
		}
		classification := ClassifyTask(ClassificationFact{Managed: true, Phase: job.Phase, Intent: job.ActivityIntent, ProblemCode: job.ProblemCode, NativeAbsent: true})
		read.Downloads.Stopped = append(read.Downloads.Stopped, aria2.Download{GID: gid, Status: "absent", Dir: job.TargetDir, Name: firstNonempty(job.PayloadRoot, job.Source), CanonicalStatus: string(classification.Status), Ownership: string(classification.Ownership), Phase: classification.Phase, ProblemCode: projectedProblemCode(job, true), Actions: session.availableActions(classification, true, job)})
	}
	if query.DetailGID != "" && read.Detail == nil && aria2.IsNotFound(read.DetailErr) {
		managedRow, absenceKnown := read.Managed[query.DetailGID]
		if job, ok := managed[query.DetailGID]; ok && absenceKnown && managedRow == nil {
			classification := ClassifyTask(ClassificationFact{Managed: true, Phase: job.Phase, Intent: job.ActivityIntent, ProblemCode: job.ProblemCode, NativeAbsent: true})
			read.Detail = &aria2.DownloadDetail{
				GID:             job.ID,
				Status:          "absent",
				Name:            firstNonempty(job.PayloadRoot, job.Source),
				PrimaryURI:      job.Source,
				DownloadDir:     job.TargetDir,
				CanonicalStatus: string(classification.Status),
				Ownership:       string(classification.Ownership),
				Phase:           classification.Phase,
				ProblemCode:     projectedProblemCode(job, true),
				Actions:         session.availableActions(classification, true, job),
			}
			read.DetailErr = nil
			read.DetailSourceErr = nil
		}
	}
	read.Downloads.Stopped = pageManagedHistory(read.Downloads.Stopped, managed, query.List)
	return read
}

func (session *DashboardSession) decorateDetail(detail *aria2.DownloadDetail, job jobs.Job, managed bool) {
	fact := ClassificationFact{
		Managed:        managed,
		NativeStatus:   detail.Status,
		NativeSeeder:   detail.Seeder,
		NativeMetadata: detail.IsMetadata,
	}
	if managed {
		fact.Phase = job.Phase
		fact.Intent = job.ActivityIntent
		fact.ProblemCode = job.ProblemCode
		fact.IdentityConflict = session.nativeDirConflict(job, detail.DownloadDir)
	}
	classification := ClassifyTask(fact)
	detail.CanonicalStatus = string(classification.Status)
	detail.Ownership = string(classification.Ownership)
	detail.Phase = classification.Phase
	detail.ProblemCode = job.ProblemCode
	detail.Actions = session.availableActions(classification, managed, job)
}

func projectedProblemCode(job jobs.Job, nativeAbsent bool) string {
	if job.ProblemCode != "" {
		return job.ProblemCode
	}
	if nativeAbsent && job.Phase == jobs.PhasePublishing {
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
	if managed && job.Phase == jobs.PhasePublishing {
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
		if !managed || (job.Phase != jobs.PhasePublishing && job.Phase != jobs.PhaseRemoved) {
			actions = append(actions, "remove")
		}
	case StatusComplete:
		if managed && session.hasMetainfo(job.ID) {
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
	if nativeDir == "" || job.Phase == jobs.PhaseRemoved {
		return false
	}
	expected := job.TargetDir
	if job.Phase != jobs.PhasePublished {
		scope, err := jobs.New(session.app.options.Paths.StateDir).LoadStorage(job.StorageID)
		if err != nil {
			return true
		}
		expected = jobs.WorkDir(scope, job.ID)
	}
	return filepath.Clean(nativeDir) != filepath.Clean(expected)
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
	detail, err := session.rpc.TaskDetail(ctx, session.identity, gid)
	if err != nil {
		return detail, err
	}
	if job, _, loadErr := jobs.New(session.app.options.Paths.StateDir).Load(gid); loadErr == nil {
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
	return AddResult{GID: result.Task.GID, Warning: result.Warning}, nil
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
