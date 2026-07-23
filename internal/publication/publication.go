// Package publication owns the filesystem half of managed publication. It
// validates paths without following the payload root, proves same-mount
// placement, performs one guarded portable rename, and reports directory-sync
// capability separately from rename success. Existing destinations are checked
// before the move; concurrent external target writers are outside the contract.
package publication

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/amio/aria2s/internal/atomicfile"
)

var (
	ErrConflict        = errors.New("publication destination already exists")
	ErrCrossDevice     = errors.New("publication paths are on different filesystems")
	ErrMountRootTarget = errors.New("target directory cannot be a mount root")
)

type Identity struct {
	MountID              uint64
	ObjectID             uint64
	ReliableAcrossRename bool
}

type Target struct {
	Path       string
	MountPoint string
	Identity   Identity
}

type MoveResult struct {
	DirectorySyncUnsupported bool
}

func InspectTarget(path string) (Target, error) {
	physical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Target{}, err
	}
	physical, err = filepath.Abs(physical)
	if err != nil {
		return Target{}, err
	}
	info, err := os.Lstat(physical)
	if err != nil {
		return Target{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Target{}, errors.New("target is not a physical directory")
	}
	identity, err := Identify(physical)
	if err != nil {
		return Target{}, err
	}
	mountPoint, err := findMountPoint(physical, identity.MountID)
	if err != nil {
		return Target{}, err
	}
	if physical == mountPoint {
		return Target{}, ErrMountRootTarget
	}
	return Target{Path: physical, MountPoint: mountPoint, Identity: identity}, nil
}

func Identify(path string) (Identity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Identity{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Identity{}, errors.New("identity path is a symlink")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Identity{}, errors.New("filesystem identity is unavailable")
	}
	reliable, err := identityReliable(path)
	if err != nil {
		return Identity{}, err
	}
	return Identity{MountID: uint64(stat.Dev), ObjectID: stat.Ino, ReliableAcrossRename: reliable}, nil
}

func SameObject(left, right Identity) bool {
	return left.MountID == right.MountID && left.ObjectID != 0 && left.ObjectID == right.ObjectID
}

func ValidatePayloadRoot(workDir, relative string) (string, Identity, error) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", Identity{}, errors.New("payload root must be relative")
	}
	clean := filepath.Clean(relative)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || filepath.Base(clean) != clean {
		return "", Identity{}, errors.New("payload root escapes work directory")
	}
	path := filepath.Join(workDir, clean)
	identity, err := identifyPayloadAt(workDir, clean)
	return path, identity, err
}

func Move(source, destination string) (MoveResult, error) {
	sourceIdentity, err := Identify(source)
	if err != nil {
		return MoveResult{}, err
	}
	destinationParent := filepath.Dir(destination)
	parentIdentity, err := Identify(destinationParent)
	if err != nil {
		return MoveResult{}, err
	}
	if sourceIdentity.MountID != parentIdentity.MountID {
		return MoveResult{}, ErrCrossDevice
	}
	return MoveExpected(source, destination, sourceIdentity, parentIdentity)
}

// MoveExpected binds a portable rename to identities observed before the
// publication transaction. The destination preflight avoids intentional
// replacement, but an external writer can still race the ordinary rename.
func MoveExpected(source, destination string, sourceIdentity, destinationParentIdentity Identity) (MoveResult, error) {
	if sourceIdentity.MountID != destinationParentIdentity.MountID {
		return MoveResult{}, ErrCrossDevice
	}
	currentSource, err := Identify(source)
	if err != nil {
		return MoveResult{}, err
	}
	if !SameObject(currentSource, sourceIdentity) {
		return MoveResult{}, errors.New("publication source identity changed")
	}
	destinationParent := filepath.Dir(destination)
	currentParent, err := Identify(destinationParent)
	if err != nil {
		return MoveResult{}, err
	}
	if !SameObject(currentParent, destinationParentIdentity) {
		return MoveResult{}, errors.New("publication destination parent identity changed")
	}
	if _, err := os.Lstat(destination); err == nil {
		return MoveResult{}, ErrConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return MoveResult{}, err
	}
	if err := os.Rename(source, destination); err != nil {
		switch {
		case errors.Is(err, syscall.EEXIST), errors.Is(err, syscall.ENOTEMPTY):
			return MoveResult{}, ErrConflict
		case errors.Is(err, syscall.EXDEV):
			return MoveResult{}, ErrCrossDevice
		default:
			return MoveResult{}, err
		}
	}
	result := MoveResult{}
	for _, parent := range []string{filepath.Dir(source), destinationParent} {
		if err := atomicfile.SyncDirectory(parent); isDirectorySyncUnsupported(err) {
			result.DirectorySyncUnsupported = true
		} else if err != nil {
			return result, fmt.Errorf("sync publication directory: %w", err)
		}
	}
	return result, nil
}

func findMountPoint(path string, device uint64) (string, error) {
	current := path
	for {
		parent := filepath.Dir(current)
		if parent == current {
			return current, nil
		}
		identity, err := Identify(parent)
		if err != nil {
			return "", err
		}
		if identity.MountID != device {
			return current, nil
		}
		current = parent
	}
}
