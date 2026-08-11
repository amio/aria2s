//go:build linux

package publication

import "testing"

func TestStableStorageIDUsesPortableFallbackOnLinux(t *testing.T) {
	if id, supported, err := StableStorageID(t.TempDir()); err != nil || supported || id != "" {
		t.Fatalf("stable ID = %q supported=%t err=%v", id, supported, err)
	}
}
