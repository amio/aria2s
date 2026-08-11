package runtime

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/amio/aria2s/internal/atomicfile"
	"golang.org/x/sys/unix"
)

const (
	ManagedLogMaxBytes  int64 = 50 * 1024 * 1024
	ManagedLogFileCount       = 3
)

// ActivateLogs rotates bounded managed logs before binding the process-wide
// stdout and stderr descriptors inherited by aria2c.
func ActivateLogs(stdoutPath, stderrPath string) error {
	err := activateLogs(stdoutPath, stderrPath)
	if err != nil {
		_ = appendActivationFailure(stderrPath, err)
	}
	return err
}

func activateLogs(stdoutPath, stderrPath string) error {
	if stdoutPath == "" || stderrPath == "" || stdoutPath == stderrPath {
		return errors.New("managed stdout and stderr log paths must be distinct")
	}
	for _, path := range []string{stdoutPath, stderrPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create managed log directory: %w", err)
		}
		if err := RotateLog(path, ManagedLogMaxBytes, ManagedLogFileCount); err != nil {
			return fmt.Errorf("rotate %s: %w", path, err)
		}
	}
	stdout, err := openLog(stdoutPath)
	if err != nil {
		return err
	}
	defer stdout.Close()
	stderr, err := openLog(stderrPath)
	if err != nil {
		return err
	}
	defer stderr.Close()
	if err := unix.Dup2(int(stdout.Fd()), int(os.Stdout.Fd())); err != nil {
		return fmt.Errorf("bind managed stdout: %w", err)
	}
	if err := unix.Dup2(int(stderr.Fd()), int(os.Stderr.Fd())); err != nil {
		return fmt.Errorf("bind managed stderr: %w", err)
	}
	return nil
}

func appendActivationFailure(path string, cause error) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_APPEND|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	_, err = fmt.Fprintf(file, "aria2s: managed log activation failed: %v\n", cause)
	return err
}

// RotateLog applies a startup-time size threshold while retaining the active
// file and numbered archives .1 through .(fileCount-1).
func RotateLog(path string, maxBytes int64, fileCount int) error {
	if path == "" || maxBytes <= 0 || fileCount < 1 {
		return errors.New("invalid managed log rotation policy")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return validateAndCleanArchives(path, maxBytes, fileCount)
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("active managed log is not a regular file")
	}
	if err := validateAndCleanArchives(path, maxBytes, fileCount); err != nil {
		return err
	}
	if info.Size() < maxBytes {
		return nil
	}
	if info.Size() > maxBytes {
		if err := trimLogTail(path, info, maxBytes); err != nil {
			return err
		}
	}
	if fileCount == 1 {
		if err := os.Truncate(path, 0); err != nil {
			return err
		}
		return atomicfile.SyncDirectory(filepath.Dir(path))
	}
	oldest := numberedLogPath(path, fileCount-1)
	if err := os.Remove(oldest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for index := fileCount - 2; index >= 1; index-- {
		from := numberedLogPath(path, index)
		if _, err := os.Lstat(from); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := os.Rename(from, numberedLogPath(path, index+1)); err != nil {
			return err
		}
	}
	if err := os.Rename(path, numberedLogPath(path, 1)); err != nil {
		return err
	}
	return atomicfile.SyncDirectory(filepath.Dir(path))
}

func validateAndCleanArchives(path string, maxBytes int64, fileCount int) error {
	directory := filepath.Dir(path)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	prefix := filepath.Base(path) + "."
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		index, err := strconv.Atoi(strings.TrimPrefix(entry.Name(), prefix))
		if err != nil || index < 1 {
			continue
		}
		archivePath := filepath.Join(directory, entry.Name())
		info, err := os.Lstat(archivePath)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed log archive %s is not a regular file", archivePath)
		}
		if index >= fileCount {
			if err := os.Remove(archivePath); err != nil {
				return err
			}
			continue
		}
		if info.Size() > maxBytes {
			if err := trimLogTail(archivePath, info, maxBytes); err != nil {
				return err
			}
		}
	}
	return nil
}

func trimLogTail(path string, info os.FileInfo, maxBytes int64) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	source := os.NewFile(uintptr(fd), path)
	defer source.Close()
	if _, err := source.Seek(info.Size()-maxBytes, io.SeekStart); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".trim-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if _, err := io.Copy(temporary, source); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	committed = true
	return atomicfile.SyncDirectory(filepath.Dir(path))
}

func numberedLogPath(path string, index int) string {
	return path + "." + strconv.Itoa(index)
}

func openLog(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_APPEND|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open managed log %s: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("managed log %s is not a regular file", path)
	}
	return file, nil
}
