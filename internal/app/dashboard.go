package app

import (
	"context"
	"fmt"

	"github.com/amio/aria2s/internal/aria2"
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
	return session.rpc.DashboardSnapshot(ctx, session.identity, query)
}

func (session *DashboardSession) TaskDetail(ctx context.Context, gid string) (aria2.DownloadDetail, error) {
	ctx, cancel := context.WithTimeout(ctx, session.app.options.DashboardReadTimeout)
	defer cancel()
	return session.rpc.TaskDetail(ctx, session.identity, gid)
}

func (session *DashboardSession) AddURI(ctx context.Context, uri string, options aria2.AddOptions) (AddResult, error) {
	ctx, cancel := context.WithTimeout(ctx, session.app.options.DashboardMutationTimeout)
	defer cancel()
	gid, err := session.rpc.AddURI(ctx, session.identity, uri, options)
	if err != nil {
		return AddResult{}, err
	}
	result := AddResult{GID: gid}
	if options.Dir != "" {
		result.Warning = session.app.recordDir(options.Dir)
	}
	return result, nil
}

func (session *DashboardSession) RecentDirs(ctx context.Context) ([]string, error) {
	return session.app.RecentDirs(ctx)
}

func (session *DashboardSession) DefaultDir() string { return session.app.DefaultDir() }

func (session *DashboardSession) Pause(ctx context.Context, gid string) error {
	return session.mutate(ctx, func(ctx context.Context) error { return session.rpc.Pause(ctx, session.identity, gid) })
}

func (session *DashboardSession) Resume(ctx context.Context, gid string) error {
	return session.mutate(ctx, func(ctx context.Context) error { return session.rpc.Resume(ctx, session.identity, gid) })
}

func (session *DashboardSession) Remove(ctx context.Context, gid string) error {
	return session.mutate(ctx, func(ctx context.Context) error { return session.rpc.Remove(ctx, session.identity, gid) })
}

func (session *DashboardSession) ClearStopped(ctx context.Context, gid string) error {
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
