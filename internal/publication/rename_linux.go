//go:build linux

package publication

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func renameNoReplace(sourceParent, sourceName, destinationParent, destinationName string, expectedSource, expectedDestinationParent Identity) (MoveResult, error) {
	sourceFD, err := unix.Open(sourceParent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return MoveResult{}, err
	}
	defer unix.Close(sourceFD)
	destinationFD, err := unix.Open(destinationParent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return MoveResult{}, err
	}
	defer unix.Close(destinationFD)
	var source, destination unix.Stat_t
	if err := unix.Fstatat(sourceFD, sourceName, &source, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return MoveResult{}, err
	}
	if source.Mode&unix.S_IFMT == unix.S_IFLNK {
		return MoveResult{}, errors.New("publication source is a symlink")
	}
	if uint64(source.Dev) != expectedSource.MountID || source.Ino != expectedSource.ObjectID {
		return MoveResult{}, errors.New("publication source identity changed")
	}
	if err := unix.Fstat(destinationFD, &destination); err != nil {
		return MoveResult{}, err
	}
	if uint64(destination.Dev) != expectedDestinationParent.MountID || destination.Ino != expectedDestinationParent.ObjectID {
		return MoveResult{}, errors.New("publication destination parent identity changed")
	}
	if uint64(source.Dev) != uint64(destination.Dev) {
		return MoveResult{}, ErrCrossDevice
	}
	err = unix.Renameat2(sourceFD, sourceName, destinationFD, destinationName, unix.RENAME_NOREPLACE)
	switch {
	case err == nil:
		unsupported := false
		for _, fd := range []int{sourceFD, destinationFD} {
			if syncErr := unix.Fsync(fd); isDirectorySyncUnsupported(syncErr) {
				unsupported = true
			} else if syncErr != nil {
				return MoveResult{}, fmt.Errorf("sync publication directory: %w", syncErr)
			}
		}
		return MoveResult{DirectorySyncUnsupported: unsupported}, nil
	case errors.Is(err, syscall.EEXIST), errors.Is(err, syscall.ENOTEMPTY):
		return MoveResult{}, ErrConflict
	case errors.Is(err, syscall.EXDEV):
		return MoveResult{}, ErrCrossDevice
	case errors.Is(err, syscall.ENOSYS), errors.Is(err, syscall.ENOTSUP), errors.Is(err, syscall.EINVAL):
		return MoveResult{}, ErrUnsupported
	default:
		return MoveResult{}, err
	}
}

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
