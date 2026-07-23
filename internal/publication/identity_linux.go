//go:build linux

package publication

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func identifyPayloadAt(parent, name string) (Identity, error) {
	fd, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return Identity{}, err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstatat(fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return Identity{}, err
	}
	if stat.Mode&unix.S_IFMT == unix.S_IFLNK {
		return Identity{}, errors.New("payload root is a symlink")
	}
	reliable, err := identityReliable(parent)
	return Identity{MountID: uint64(stat.Dev), ObjectID: stat.Ino, ReliableAcrossRename: reliable}, err
}

func identityReliable(path string) (bool, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return false, err
	}
	switch stat.Type {
	case unix.EXT4_SUPER_MAGIC, unix.XFS_SUPER_MAGIC, unix.BTRFS_SUPER_MAGIC, unix.TMPFS_MAGIC:
		return true, nil
	case unix.NFS_SUPER_MAGIC, unix.CIFS_SUPER_MAGIC:
		return false, nil
	default:
		return false, nil
	}
}

func isDirectorySyncUnsupported(err error) bool {
	return errors.Is(err, os.ErrInvalid) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP)
}
