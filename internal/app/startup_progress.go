package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type startupProgressPhase string

const (
	startupPhaseStarting   startupProgressPhase = "starting"
	startupPhaseChecking   startupProgressPhase = "checking"
	startupPhaseWaitingRPC startupProgressPhase = "waiting-rpc"
)

type startupProgress struct {
	phase   startupProgressPhase
	current int
	total   int
}

func (progress startupProgress) message() string {
	switch progress.phase {
	case startupPhaseStarting:
		return "Starting aria2…"
	case startupPhaseChecking:
		return fmt.Sprintf("Checking task %d of %d…", progress.current, progress.total)
	case startupPhaseWaitingRPC:
		return "Waiting for aria2 RPC…"
	default:
		return ""
	}
}

func (progress startupProgress) encode() ([]byte, error) {
	switch progress.phase {
	case startupPhaseStarting, startupPhaseWaitingRPC:
		return []byte(string(progress.phase) + "\n"), nil
	case startupPhaseChecking:
		if progress.current < 1 || progress.total < progress.current {
			return nil, errors.New("invalid startup task progress")
		}
		return []byte(fmt.Sprintf("checking %d %d\n", progress.current, progress.total)), nil
	default:
		return nil, errors.New("invalid startup progress phase")
	}
}

func parseStartupProgress(data []byte) (startupProgress, error) {
	fields := strings.Fields(string(data))
	if len(fields) == 1 {
		progress := startupProgress{phase: startupProgressPhase(fields[0])}
		if progress.phase != startupPhaseStarting && progress.phase != startupPhaseWaitingRPC {
			return startupProgress{}, errors.New("invalid startup progress phase")
		}
		return progress, nil
	}
	if len(fields) != 3 || fields[0] != string(startupPhaseChecking) {
		return startupProgress{}, errors.New("invalid startup progress")
	}
	current, err := strconv.Atoi(fields[1])
	if err != nil {
		return startupProgress{}, errors.New("invalid startup task index")
	}
	total, err := strconv.Atoi(fields[2])
	if err != nil || current < 1 || total < current {
		return startupProgress{}, errors.New("invalid startup task count")
	}
	return startupProgress{phase: startupPhaseChecking, current: current, total: total}, nil
}

func writeStartupProgress(path string, progress startupProgress) error {
	data, err := progress.encode()
	if err != nil {
		return err
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(parent, ".startup-progress-*.tmp")
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
	if err := temp.Chmod(0o600); err != nil {
		return closeWith(err)
	}
	if _, err := temp.Write(data); err != nil {
		return closeWith(err)
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func readStartupProgress(path string) (startupProgress, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return startupProgress{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return startupProgress{}, errors.New("startup progress is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return startupProgress{}, err
	}
	return parseStartupProgress(data)
}

func removeStartupProgress(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

type dashboardStartupError struct {
	cause    error
	progress startupProgress
}

func (err *dashboardStartupError) Error() string { return err.cause.Error() }
func (err *dashboardStartupError) Unwrap() error { return err.cause }
func (err *dashboardStartupError) StartupMessage() string {
	return err.progress.message()
}

var persistStartupProgress = writeStartupProgress
var clearStartupProgress = removeStartupProgress
