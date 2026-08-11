//go:build darwin

package publication

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
