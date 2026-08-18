//go:build darwin

package app

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type darwinStorageReconnecter struct{}

func newPlatformStorageReconnecter() StorageReconnecter { return darwinStorageReconnecter{} }

func (darwinStorageReconnecter) Observe(mountPoint string) (string, bool, error) {
	mounts, err := darwinMounts()
	if err != nil {
		return "", false, err
	}
	want := filepath.Clean(mountPoint)
	for _, mount := range mounts {
		if filepath.Clean(byteString(mount.Mntonname[:])) != want {
			continue
		}
		if byteString(mount.Fstypename[:]) != "smbfs" {
			return "", true, nil
		}
		reconnectURL, err := normalizeSMBReconnectURL(byteString(mount.Mntfromname[:]))
		return reconnectURL, true, err
	}
	return "", false, nil
}

func (darwinStorageReconnecter) Request(ctx context.Context, reconnectURL string) error {
	normalized, err := normalizeSMBReconnectURL(reconnectURL)
	if err != nil {
		return err
	}
	return exec.CommandContext(ctx, "/usr/bin/open", "-g", "-b", "com.apple.finder", normalized).Run()
}

func darwinMounts() ([]unix.Statfs_t, error) {
	count, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil {
		return nil, err
	}
	for range 2 {
		mounts := make([]unix.Statfs_t, count+8)
		observed, err := unix.Getfsstat(mounts, unix.MNT_NOWAIT)
		if err != nil {
			return nil, err
		}
		if observed < len(mounts) {
			return mounts[:observed], nil
		}
		count = observed
	}
	return nil, errors.New("mount table changed while reading")
}

func byteString(value []byte) string {
	if end := strings.IndexByte(string(value), 0); end >= 0 {
		value = value[:end]
	}
	return string(value)
}
