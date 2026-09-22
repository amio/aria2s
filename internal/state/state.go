// Package state persists the committed managed-runtime identity. Schema v2 is
// activated only after its controller, service artifact, and versioned session
// locations are prepared and can be validated at process startup.
// Updates lock a stable sibling file, then reread and atomically replace state;
// locking the state inode itself would lose coordination after replacement.
package state

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"github.com/amio/aria2s/internal/atomicfile"
	"golang.org/x/sys/unix"
)

/** State is the authoritative local runtime metadata for aria2s-managed RPC access. */
type State struct {
	RuntimeSchemaVersion int      `json:"runtimeSchemaVersion"`
	ControllerPath       string   `json:"controllerPath"`
	ControllerIdentity   string   `json:"controllerIdentity"`
	ServiceIdentity      string   `json:"serviceIdentity"`
	Aria2cPath           string   `json:"aria2cPath"`
	RPCPort              int      `json:"rpcPort"`
	RPCSecret            string   `json:"rpcSecret"`
	SessionPath          string   `json:"sessionPath"`
	StartupInputPath     string   `json:"startupInputPath"`
	LogPath              string   `json:"logPath"`
	ErrorLogPath         string   `json:"errorLogPath"`
	ServiceName          string   `json:"serviceName"`
	RecentDirs           []string `json:"recentDirs,omitempty"`
}

var ErrRuntimeChanged = errors.New("managed runtime changed during update; retry the operation")

// SameRuntime compares the committed runtime independently of user preferences.
func (current State) SameRuntime(other State) bool {
	current.RecentDirs, other.RecentDirs = nil, nil
	return reflect.DeepEqual(current, other)
}

// Update applies a short mutation to the latest existing state. The callback
// must not perform supervisor/RPC work or recursively update the same file.
func Update(ctx context.Context, path string, mutate func(*State) error) (State, error) {
	return update(ctx, path, false, mutate)
}

// CommitRuntime rejects proposals based on a superseded runtime and preserves
// the latest preferences. A nil expected state permits first-install creation.
func CommitRuntime(ctx context.Context, path string, expected *State, desired State) (State, error) {
	return update(ctx, path, expected == nil, func(current *State) error {
		if expected != nil && !current.SameRuntime(*expected) {
			return ErrRuntimeChanged
		}
		desired.RecentDirs = current.RecentDirs
		*current = desired
		return nil
	})
}

func update(ctx context.Context, path string, create bool, mutate func(*State) error) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	if create {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return State{}, err
		}
	}
	// The lock remains at a stable inode across writes and process lifetimes.
	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return State{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return State{}, err
	}
	if !info.Mode().IsRegular() {
		return State{}, errors.New("runtime state lock is not a regular file")
	}
	for {
		if err := ctx.Err(); err != nil {
			return State{}, err
		}
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			return State{}, err
		}
		select {
		case <-ctx.Done():
			return State{}, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	current, err := Load(path)
	exists := err == nil
	if err != nil && !(create && errors.Is(err, os.ErrNotExist)) {
		return State{}, err
	}
	if create && exists {
		return State{}, ErrRuntimeChanged
	}
	next := current
	next.RecentDirs = slices.Clone(current.RecentDirs)
	if err := mutate(&next); err != nil {
		return State{}, err
	}
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	if exists && next.SameRuntime(current) && slices.Equal(next.RecentDirs, current.RecentDirs) {
		return current, nil
	}
	if err := Save(path, next); err != nil {
		return State{}, err
	}
	return next, nil
}

// Save writes a complete state without coordinating concurrent updates. It is
// reserved for fixtures; production writers use Update or CommitRuntime.
func Save(path string, current State) error {
	data, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, data, 0o600)
}

func Load(path string) (State, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return State{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return State{}, errors.New("runtime state is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	var current State
	if err := json.Unmarshal(data, &current); err != nil {
		return State{}, err
	}
	return current, nil
}
