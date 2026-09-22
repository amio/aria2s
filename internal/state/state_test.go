package state_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/amio/aria2s/internal/paths"
	"github.com/amio/aria2s/internal/state"
)

func TestSaveStateWrites0600AndRoundTrips(t *testing.T) {
	root := t.TempDir()
	servicePaths := paths.NewDarwin(filepath.Join(root, "home"))
	current := state.State{
		Aria2cPath:   "/opt/homebrew/bin/aria2c",
		RPCPort:      6800,
		RPCSecret:    "secret-token",
		SessionPath:  servicePaths.SessionFile,
		LogPath:      servicePaths.LogFile,
		ErrorLogPath: servicePaths.ErrorLogFile,
		ServiceName:  "io.github.amio.aria2s",
	}

	if err := state.Save(servicePaths.StateFile, current); err != nil {
		t.Fatalf("save state: %v", err)
	}

	info, err := os.Stat(servicePaths.StateFile)
	if err != nil {
		t.Fatalf("stat state: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("expected 0600, got %o", got)
	}

	reloaded, err := state.Load(servicePaths.StateFile)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if !reflect.DeepEqual(reloaded, current) {
		t.Fatalf("round trip mismatch:\nwant %#v\n got %#v", current, reloaded)
	}
}

func TestUpdateSerializesAcrossProcessesAndHonorsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	initial := state.State{ControllerIdentity: "old", RecentDirs: []string{"/initial"}}
	if err := state.Save(path, initial); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStateUpdateProcessHelper$")
	child.Env = append(os.Environ(), "ARIA2S_STATE_UPDATE_TEST="+path)
	child.Stderr = os.Stderr
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Wait()
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "locked\n" {
		t.Fatalf("child did not acquire lock: %q, %v", line, err)
	}
	lockInfo, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancelWait := context.WithTimeout(ctx, 75*time.Millisecond)
	defer cancelWait()
	called := false
	_, err = state.Update(waitCtx, path, func(*state.State) error {
		called = true
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatalf("blocked update did not cancel before callback: called=%v err=%v", called, err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := state.Update(ctx, path, func(current *state.State) error {
			if current.ControllerIdentity != "new" {
				return fmt.Errorf("runtime was not reread: %q", current.ControllerIdentity)
			}
			current.RecentDirs = append(current.RecentDirs, "/parent")
			return nil
		})
		result <- err
	}()
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatalf("child update: %v", err)
	}
	if err := <-result; err != nil {
		t.Fatalf("parent update: %v", err)
	}
	updated, err := state.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(updated.RecentDirs, []string{"/initial", "/child", "/parent"}) || updated.ControllerIdentity != "new" {
		t.Fatalf("serialized changes lost: %#v", updated)
	}
	newLockInfo, err := os.Stat(path + ".lock")
	if err != nil || !os.SameFile(lockInfo, newLockInfo) {
		t.Fatalf("state replacement changed the lock inode: %v", err)
	}
}

func TestStateUpdateProcessHelper(t *testing.T) {
	path := os.Getenv("ARIA2S_STATE_UPDATE_TEST")
	if path == "" {
		return
	}
	_, err := state.Update(context.Background(), path, func(current *state.State) error {
		fmt.Fprintln(os.Stdout, "locked")
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			return err
		}
		current.ControllerIdentity = "new"
		current.RecentDirs = append(current.RecentDirs, "/child")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeCommitPreservesLatestPreferencesAndRejectsStaleProposals(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	initial := state.State{RuntimeSchemaVersion: 2, ControllerIdentity: "old", RPCSecret: "secret"}
	if _, err := state.CommitRuntime(ctx, path, nil, initial); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Update(ctx, path, func(current *state.State) error {
		current.RecentDirs = []string{"/new"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	desired := initial
	desired.ControllerIdentity = "new"
	committed, err := state.CommitRuntime(ctx, path, &initial, desired)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(committed.RecentDirs, []string{"/new"}) || committed.ControllerIdentity != "new" {
		t.Fatalf("runtime commit reverted preferences: %#v", committed)
	}
	desired.RPCSecret = "stale"
	if _, err := state.CommitRuntime(ctx, path, &initial, desired); !errors.Is(err, state.ErrRuntimeChanged) {
		t.Fatalf("stale proposal accepted: %v", err)
	}
	if _, err := state.CommitRuntime(ctx, path, nil, desired); !errors.Is(err, state.ErrRuntimeChanged) {
		t.Fatalf("first install replaced existing state: %v", err)
	}
	updated, err := state.Load(path)
	if err != nil || !reflect.DeepEqual(updated, committed) {
		t.Fatalf("rejected proposal changed state: %#v, %v", updated, err)
	}
}

func TestUpdateNoopAbortAndCancellationPreserveFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	initial := state.State{RPCSecret: "secret"}
	if err := state.Save(path, initial); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := state.Update(ctx, path, func(current *state.State) error {
		current.RecentDirs = []string{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.CommitRuntime(ctx, path, &initial, initial); err != nil {
		t.Fatal(err)
	}
	abort := errors.New("abort mutation")
	if _, err := state.Update(ctx, path, func(current *state.State) error {
		current.RPCSecret = "should not save"
		return abort
	}); !errors.Is(err, abort) {
		t.Fatalf("callback abort: %v", err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	if _, err := state.Update(cancelCtx, path, func(current *state.State) error {
		current.RPCSecret = "should not save"
		cancel()
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled mutation: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("no-op or aborted mutation replaced state: %v", err)
	}
	updated, err := state.Load(path)
	if err != nil || !reflect.DeepEqual(updated, initial) {
		t.Fatalf("no-op or aborted mutation changed state: %#v, %v", updated, err)
	}
}

func TestUpdateRejectsMissingOrInvalidState(t *testing.T) {
	for _, kind := range []string{"missing", "corrupt", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			switch kind {
			case "corrupt":
				if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := state.Save(path+".target", state.State{}); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".target", path); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			if _, err := state.Update(context.Background(), path, func(*state.State) error {
				called = true
				return nil
			}); err == nil || called {
				t.Fatalf("invalid state reached callback: called=%v err=%v", called, err)
			}
			expected := state.State{}
			if _, err := state.CommitRuntime(context.Background(), path, &expected, expected); err == nil {
				t.Fatal("runtime update accepted invalid/missing state")
			}
		})
	}
}
