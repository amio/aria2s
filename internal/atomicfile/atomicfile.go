// Package atomicfile owns the crash-durable replacement primitive used by
// aria2s control state. A successful write has reached the file and its parent
// directory; callers never observe a partially encoded authoritative file.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

func Write(path string, data []byte, mode os.FileMode) error {
	return write(path, data, mode, nil)
}

func write(path string, data []byte, mode os.FileMode, step func(string)) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(parent, ".aria2s-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	closeWith := func(cause error) error {
		if closeErr := temp.Close(); cause == nil {
			return closeErr
		}
		return cause
	}
	if err := temp.Chmod(mode); err != nil {
		return closeWith(err)
	}
	if _, err := temp.Write(data); err != nil {
		return closeWith(err)
	}
	if err := temp.Sync(); err != nil {
		return closeWith(err)
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if step != nil {
		step("before-rename")
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	if step != nil {
		step("after-rename")
	}
	return SyncDirectory(parent)
}

// Create installs a complete file only when path does not already exist.
func Create(path string, data []byte, mode os.FileMode) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(parent, ".aria2s-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Link(tempPath, path); err != nil {
		return err
	}
	if err := os.Remove(tempPath); err != nil {
		return err
	}
	return SyncDirectory(parent)
}

// SyncDirectory makes a preceding create, rename, or removal durable.
func SyncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		directory.Close()
		return fmt.Errorf("sync directory %s: %w", path, err)
	}
	return directory.Close()
}
