//go:build linux

package publication

// StableStorageID deliberately reports no native identifier on Linux. Local
// block UUIDs and remote-export identities require different providers, so the
// app uses its portable on-storage marker instead of treating mount IDs or
// device paths as durable.
func StableStorageID(string) (string, bool, error) { return "", false, nil }
