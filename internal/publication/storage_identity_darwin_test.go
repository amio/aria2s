//go:build darwin

package publication

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestStableStorageIDFallsBackWhenVolumeUUIDIsUnsupported(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.EINVAL, syscall.ENOTSUP} {
		id, supported, err := decodeStableStorageID(make([]byte, 20), errno)
		if err != nil || supported || id != "" {
			t.Fatalf("errno %v: id=%q supported=%t err=%v", errno, id, supported, err)
		}
	}

	_, _, err := decodeStableStorageID(make([]byte, 20), syscall.EPERM)
	if !os.IsPermission(err) {
		t.Fatalf("permission error = %v", err)
	}
}

func TestStableStorageIDIsSharedByPathsOnVolume(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	rootID, rootSupported, err := StableStorageID(root)
	if err != nil {
		t.Fatal(err)
	}
	childID, childSupported, err := StableStorageID(child)
	if err != nil {
		t.Fatal(err)
	}
	if !rootSupported || !childSupported || rootID == "" || rootID != childID || !strings.HasPrefix(rootID, "darwin-volume:") {
		t.Fatalf("root=%q/%t child=%q/%t", rootID, rootSupported, childID, childSupported)
	}
}
