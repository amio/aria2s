//go:build darwin

package app

import "testing"

func TestDarwinReconnecterDistinguishesExactMountPoint(t *testing.T) {
	connector := darwinStorageReconnecter{}
	if reconnectURL, mounted, err := connector.Observe("/"); err != nil || !mounted || reconnectURL != "" {
		t.Fatalf("root observation = url %q mounted %v err %v", reconnectURL, mounted, err)
	}
	if reconnectURL, mounted, err := connector.Observe(t.TempDir()); err != nil || mounted || reconnectURL != "" {
		t.Fatalf("ordinary directory observation = url %q mounted %v err %v", reconnectURL, mounted, err)
	}
}
