// Package publication owns the filesystem half of managed publication. It
// validates paths without following the payload root, proves same-mount
// placement, performs one kernel no-replace rename, and reports directory-sync
// capability separately from rename success.
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
	ErrUnsupported     = errors.New("kernel no-replace rename is unsupported")
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

func MoveNoReplace(source, destination string) (MoveResult, error) {
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
	return MoveNoReplaceExpected(source, destination, sourceIdentity, parentIdentity)
}

// MoveNoReplaceExpected binds the rename to identities observed before the
// publication transaction, closing path-replacement races at both parents.
func MoveNoReplaceExpected(source, destination string, sourceIdentity, destinationParentIdentity Identity) (MoveResult, error) {
	if sourceIdentity.MountID != destinationParentIdentity.MountID {
		return MoveResult{}, ErrCrossDevice
	}
	return renameNoReplace(filepath.Dir(source), filepath.Base(source), filepath.Dir(destination), filepath.Base(destination), sourceIdentity, destinationParentIdentity)
}

func ProbeNoReplace(sourceParent, destinationParent string) (result MoveResult, resultErr error) {
	source, err := os.CreateTemp(sourceParent, ".aria2s-probe-")
	if err != nil {
		return MoveResult{}, err
	}
	sourcePath := source.Name()
	defer cleanupProbePath(sourcePath, sourceParent, &result, &resultErr)
	if _, err := source.WriteString("source"); err != nil {
		source.Close()
		return MoveResult{}, err
	}
	if err := source.Close(); err != nil {
		return MoveResult{}, err
	}
	destination := filepath.Join(destinationParent, filepath.Base(sourcePath)+"-destination")
	defer cleanupProbePath(destination, destinationParent, &result, &resultErr)
	destinationFile, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return MoveResult{}, err
	}
	if _, err := destinationFile.WriteString("destination"); err != nil {
		destinationFile.Close()
		return MoveResult{}, err
	}
	if err := destinationFile.Close(); err != nil {
		return MoveResult{}, err
	}
	if _, err := MoveNoReplace(sourcePath, destination); !errors.Is(err, ErrConflict) {
		return MoveResult{}, fmt.Errorf("no-replace collision probe: %w", err)
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "destination" {
		return MoveResult{}, errors.New("no-replace collision probe modified destination")
	}
	before, err := Identify(sourcePath)
	if err != nil {
		return MoveResult{}, err
	}
	if err := os.Remove(destination); err != nil {
		return MoveResult{}, err
	}
	result, err = MoveNoReplace(sourcePath, destination)
	if err != nil {
		return MoveResult{}, err
	}
	after, err := Identify(destination)
	if err != nil {
		return MoveResult{}, err
	}
	if !SameObject(before, after) {
		return MoveResult{}, errors.New("publication probe did not preserve identity")
	}
	return result, nil
}

func cleanupProbePath(path, parent string, result *MoveResult, resultErr *error) {
	removeErr := os.Remove(path)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	syncErr := atomicfile.SyncDirectory(parent)
	if isDirectorySyncUnsupported(syncErr) {
		result.DirectorySyncUnsupported = true
		syncErr = nil
	}
	if removeErr != nil || syncErr != nil {
		*resultErr = errors.Join(*resultErr, removeErr, syncErr)
	}
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

var errDirectorySyncUnsupported = errors.New("directory sync unsupported")
