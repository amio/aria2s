package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/amio/aria2s/internal/jobs"
)

const (
	storageReconnectWait = 10 * time.Second
	storageReconnectPoll = 200 * time.Millisecond
)

func normalizeSMBReconnectURL(source string) (string, error) {
	source = strings.TrimSpace(source)
	if strings.HasPrefix(source, "//") {
		source = "smb:" + source
	}
	parsed, err := url.Parse(source)
	if err != nil || !strings.EqualFold(parsed.Scheme, "smb") || parsed.Host == "" || strings.Trim(parsed.Path, "/") == "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", errors.New("invalid SMB mount source")
	}
	var user *url.Userinfo
	if parsed.User != nil && parsed.User.Username() != "" {
		user = url.User(parsed.User.Username())
	}
	return (&url.URL{Scheme: "smb", User: user, Host: parsed.Host, Path: parsed.Path, RawPath: parsed.RawPath}).String(), nil
}

func (app *App) bindStorageReconnect(repository *jobs.Repository, scope jobs.StorageScope) (jobs.StorageScope, error) {
	if app.options.StorageReconnecter == nil {
		return scope, nil
	}
	reconnectURL, mounted, err := app.options.StorageReconnecter.Observe(scope.MountPoint)
	if err != nil {
		if mounted {
			return scope, nil
		}
		return jobs.StorageScope{}, fmt.Errorf("observe storage reconnect source: %w", err)
	}
	if !mounted || reconnectURL == "" || reconnectURL == scope.ReconnectURL {
		return scope, nil
	}
	scope.ReconnectURL = reconnectURL
	if err := repository.SaveStorage(scope); err != nil {
		return jobs.StorageScope{}, err
	}
	return scope, nil
}

// reconnectStorageForRetry performs only the potentially slow user-session
// mount request. The locked reconciler reloads and proves every durable and
// filesystem fact before it mutates the selected job.
func (app *App) reconnectStorageForRetry(ctx context.Context, repository *jobs.Repository, jobID string) error {
	if app.options.StorageReconnecter == nil {
		return nil
	}
	job, _, err := repository.Load(jobID)
	if err != nil {
		return err
	}
	if job.Removed {
		return nil
	}
	scope, err := repository.LoadStorage(job.StorageID)
	if err != nil {
		return err
	}
	if scope.ReconnectURL == "" {
		return nil
	}
	_, mounted, err := app.options.StorageReconnecter.Observe(scope.MountPoint)
	if mounted {
		return nil
	}
	if err != nil {
		return fmt.Errorf("observe registered storage mount: %w", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, storageReconnectWait)
	defer cancel()
	if err := app.options.StorageReconnecter.Request(waitCtx, scope.ReconnectURL); err != nil {
		return fmt.Errorf("request registered storage mount: %w", err)
	}
	ticker := time.NewTicker(storageReconnectPoll)
	defer ticker.Stop()
	for {
		_, mounted, err = app.options.StorageReconnecter.Observe(scope.MountPoint)
		if mounted {
			return nil
		}
		if err != nil {
			return fmt.Errorf("observe requested storage mount: %w", err)
		}
		select {
		case <-waitCtx.Done():
			if err := ctx.Err(); err != nil {
				return err
			}
			return fmt.Errorf("registered network storage did not mount at %s within %s", scope.MountPoint, storageReconnectWait)
		case <-ticker.C:
		}
	}
}
