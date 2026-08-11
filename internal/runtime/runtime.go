// Package runtime owns process-lifetime mechanics only: bounded log activation,
// the inherited instance lease, atomic startup input replacement, hook launchers,
// and final aria2 exec.
package runtime

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/amio/aria2s/internal/atomicfile"
	"golang.org/x/sys/unix"
)

const LockFDEnvironment = "ARIA2S_INSTANCE_LOCK_FD"

const safeStartupContent = "file-allocation=none\n"

// EnableSafeStartup requests a managed process whose file allocation cannot
// monopolize aria2's event loop before JSON-RPC becomes responsive.
func EnableSafeStartup(path string) error {
	return atomicfile.Write(path, []byte(safeStartupContent), 0o600)
}

func SafeStartupEnabled(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("safe-startup marker is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if string(data) != safeStartupContent {
		return false, errors.New("safe-startup marker has invalid content")
	}
	return true, nil
}

func DisableSafeStartup(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return atomicfile.SyncDirectory(filepathDir(path))
}

type Lease struct {
	file *os.File
}

func Acquire(path string) (*Lease, error) {
	if err := os.MkdirAll(filepathDir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errors.New("managed aria2 instance is already running")
		}
		return nil, err
	}
	if _, err := unix.FcntlInt(file.Fd(), unix.F_SETFD, 0); err != nil {
		file.Close()
		return nil, err
	}
	return &Lease{file: file}, nil
}

func (lease *Lease) Environment() string {
	return LockFDEnvironment + "=" + strconv.FormatUint(uint64(lease.file.Fd()), 10)
}

func (lease *Lease) Close() error {
	if lease == nil || lease.file == nil {
		return nil
	}
	return lease.file.Close()
}

func CloseInheritedLock() error {
	value := os.Getenv(LockFDEnvironment)
	if value == "" {
		return nil
	}
	fd, err := strconv.Atoi(value)
	if err != nil || fd < 3 {
		return errors.New("invalid inherited instance lock descriptor")
	}
	if err := unix.Close(fd); err != nil && !errors.Is(err, unix.EBADF) {
		return err
	}
	return os.Unsetenv(LockFDEnvironment)
}

func WriteStartup(path string, data []byte) error {
	return atomicfile.Write(path, data, 0o600)
}

func WriteHook(path, controllerPath, event string) error {
	content := fmt.Sprintf("#!/bin/sh\nexec %s managed-hook %s \"$1\"\n", shellQuote(controllerPath), shellQuote(event))
	return atomicfile.Write(path, []byte(content), 0o700)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func Exec(executable string, args, environment []string) error {
	argv := append([]string{executable}, args...)
	return syscall.Exec(executable, argv, environment)
}

func filepathDir(path string) string {
	for index := len(path) - 1; index >= 0; index-- {
		if path[index] == os.PathSeparator {
			if index == 0 {
				return string(os.PathSeparator)
			}
			return path[:index]
		}
	}
	return "."
}
