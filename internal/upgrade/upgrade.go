// Package upgrade owns the verified, atomic replacement of the aria2s
// executable from the project's fixed GitHub Release asset contract.
package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/amio/aria2s/internal/atomicfile"
)

const (
	defaultRepository            = "https://github.com/amio/aria2s"
	maxChecksumSize              = 1 << 20
	maxBinarySize                = 128 << 20
	maxCandidateOutput           = 4 << 10
	candidateVerificationTimeout = 10 * time.Second
)

type Options struct {
	CurrentVersion string
	RepositoryURL  string
	ExecutablePath string
	Client         *http.Client
}

type Result struct {
	Current string
	Latest  string
	Updated bool
}

// PrivilegeError means the executable directory must be made writable before
// any network or replacement work can begin.
type PrivilegeError struct {
	ExecutablePath string
}

func (failure *PrivilegeError) Error() string {
	return fmt.Sprintf("administrator permission is required to replace %s", failure.ExecutablePath)
}

func Run(ctx context.Context, options Options) (Result, error) {
	current, err := parseVersion(options.CurrentVersion)
	if err != nil {
		return Result{}, fmt.Errorf("self-upgrade is unavailable for development version %q", options.CurrentVersion)
	}

	repository := strings.TrimRight(options.RepositoryURL, "/")
	if repository == "" {
		repository = defaultRepository
	}
	client := options.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}

	latest, tag, err := detectLatest(ctx, client, repository)
	if err != nil {
		return Result{}, err
	}
	result := Result{Current: current.String(), Latest: latest.String()}
	if latest.compare(current) <= 0 {
		return result, nil
	}

	executablePath, err := resolveExecutable(options.ExecutablePath)
	if err != nil {
		return Result{}, err
	}
	if isHomebrewPath(executablePath) {
		return Result{}, fmt.Errorf("%s is managed by Homebrew; run `brew upgrade aria2s`", executablePath)
	}
	info, err := os.Stat(executablePath)
	if err != nil {
		return Result{}, fmt.Errorf("inspect current executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Result{}, fmt.Errorf("current executable is not a regular file: %s", executablePath)
	}

	candidate, err := os.CreateTemp(filepath.Dir(executablePath), ".aria2s-upgrade-*")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return Result{}, &PrivilegeError{ExecutablePath: executablePath}
		}
		return Result{}, fmt.Errorf("create replacement beside %s: %w", executablePath, err)
	}
	candidatePath := candidate.Name()
	defer os.Remove(candidatePath)
	closeCandidate := func(cause error) error {
		if closeErr := candidate.Close(); cause == nil {
			return closeErr
		}
		return cause
	}

	assetName := fmt.Sprintf("aria2s_%s_%s_%s", latest.String(), runtime.GOOS, runtime.GOARCH)
	assetBase := repository + "/releases/download/" + url.PathEscape(tag) + "/"
	checksums, err := downloadBytes(ctx, client, assetBase+"checksums.txt", maxChecksumSize)
	if err != nil {
		return Result{}, closeCandidate(fmt.Errorf("download checksums: %w", err))
	}
	expected, err := checksumFor(checksums, assetName)
	if err != nil {
		return Result{}, closeCandidate(err)
	}
	actual, err := downloadBinary(ctx, client, assetBase+url.PathEscape(assetName), candidate)
	if err != nil {
		return Result{}, closeCandidate(fmt.Errorf("download release binary: %w", err))
	}
	if actual != expected {
		return Result{}, closeCandidate(fmt.Errorf("checksum mismatch for %s", assetName))
	}
	mode := info.Mode().Perm()
	if mode&0o111 == 0 {
		mode |= 0o755
	}
	if err := candidate.Chmod(mode); err != nil {
		return Result{}, closeCandidate(fmt.Errorf("make replacement executable: %w", err))
	}
	if err := candidate.Sync(); err != nil {
		return Result{}, closeCandidate(fmt.Errorf("sync replacement executable: %w", err))
	}
	if err := candidate.Close(); err != nil {
		return Result{}, fmt.Errorf("close replacement executable: %w", err)
	}
	if err := verifyCandidate(ctx, candidatePath, latest, candidateVerificationTimeout); err != nil {
		return Result{}, err
	}
	if err := os.Rename(candidatePath, executablePath); err != nil {
		return Result{}, fmt.Errorf("replace executable: %w", err)
	}
	result.Updated = true
	if err := atomicfile.SyncDirectory(filepath.Dir(executablePath)); err != nil {
		return result, fmt.Errorf("aria2s was replaced but its directory could not be synced: %w", err)
	}
	return result, nil
}

type version struct {
	major uint64
	minor uint64
	patch uint64
}

func parseVersion(value string) (version, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return version{}, errors.New("version must contain three components")
	}
	values := make([]uint64, 3)
	for index, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return version{}, errors.New("invalid version component")
		}
		parsed, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return version{}, errors.New("invalid version component")
		}
		values[index] = parsed
	}
	return version{major: values[0], minor: values[1], patch: values[2]}, nil
}

func (value version) String() string {
	return fmt.Sprintf("%d.%d.%d", value.major, value.minor, value.patch)
}

func (value version) compare(other version) int {
	left := [...]uint64{value.major, value.minor, value.patch}
	right := [...]uint64{other.major, other.minor, other.patch}
	for index := range left {
		if left[index] < right[index] {
			return -1
		}
		if left[index] > right[index] {
			return 1
		}
	}
	return 0
}

func detectLatest(ctx context.Context, client *http.Client, repository string) (version, string, error) {
	repositoryURL, err := url.Parse(repository)
	if err != nil || repositoryURL.Scheme == "" || repositoryURL.Host == "" {
		return version{}, "", fmt.Errorf("invalid release repository URL %q", repository)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, repository+"/releases/latest", nil)
	if err != nil {
		return version{}, "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return version{}, "", fmt.Errorf("resolve latest release: %w", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return version{}, "", fmt.Errorf("resolve latest release: HTTP %s", response.Status)
	}
	finalURL := response.Request.URL
	prefix := strings.TrimRight(repositoryURL.Path, "/") + "/releases/tag/"
	if !strings.EqualFold(finalURL.Host, repositoryURL.Host) || !strings.HasPrefix(finalURL.Path, prefix) {
		return version{}, "", fmt.Errorf("latest release redirected to an unexpected URL: %s", finalURL.Redacted())
	}
	tag, err := url.PathUnescape(strings.TrimPrefix(finalURL.Path, prefix))
	if err != nil || tag == "" || strings.Contains(tag, "/") {
		return version{}, "", fmt.Errorf("latest release has an invalid tag URL: %s", finalURL.Redacted())
	}
	latest, err := parseVersion(tag)
	if err != nil {
		return version{}, "", fmt.Errorf("latest release tag %q is not a stable semantic version", tag)
	}
	return latest, tag, nil
}

func resolveExecutable(configured string) (string, error) {
	executablePath := configured
	var err error
	if executablePath == "" {
		executablePath, err = os.Executable()
		if err != nil {
			return "", fmt.Errorf("locate current executable: %w", err)
		}
	}
	executablePath, err = filepath.Abs(executablePath)
	if err != nil {
		return "", fmt.Errorf("resolve current executable: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(executablePath)
	if err != nil {
		return "", fmt.Errorf("resolve current executable symlinks: %w", err)
	}
	return resolved, nil
}

func isHomebrewPath(value string) bool {
	clean := filepath.ToSlash(filepath.Clean(value))
	return strings.Contains(clean, "/Cellar/") || strings.Contains(clean, "/homebrew/opt/")
}

func downloadBytes(ctx context.Context, client *http.Client, address string, limit int64) ([]byte, error) {
	response, err := get(ctx, client, address)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.ContentLength > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return data, nil
}

func get(ctx context.Context, client *http.Client, address string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("HTTP %s", response.Status)
	}
	return response, nil
}

func checksumFor(data []byte, assetName string) ([sha256.Size]byte, error) {
	var found [sha256.Size]byte
	matches := 0
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != assetName {
			continue
		}
		decoded, err := hex.DecodeString(fields[0])
		if err != nil || len(decoded) != sha256.Size {
			return found, fmt.Errorf("invalid checksum for %s", assetName)
		}
		copy(found[:], decoded)
		matches++
	}
	if matches == 0 {
		return found, fmt.Errorf("checksums.txt has no entry for %s", assetName)
	}
	if matches > 1 {
		return found, fmt.Errorf("checksums.txt has duplicate entries for %s", assetName)
	}
	return found, nil
}

func downloadBinary(ctx context.Context, client *http.Client, address string, destination *os.File) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	response, err := get(ctx, client, address)
	if err != nil {
		return sum, err
	}
	defer response.Body.Close()
	if response.ContentLength > maxBinarySize {
		return sum, fmt.Errorf("binary exceeds %d bytes", maxBinarySize)
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(destination, hash), io.LimitReader(response.Body, maxBinarySize+1))
	if err != nil {
		return sum, err
	}
	if written == 0 {
		return sum, errors.New("release binary is empty")
	}
	if written > maxBinarySize {
		return sum, fmt.Errorf("binary exceeds %d bytes", maxBinarySize)
	}
	copy(sum[:], hash.Sum(nil))
	return sum, nil
}

func verifyCandidate(ctx context.Context, executablePath string, expected version, timeout time.Duration) error {
	verificationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var output cappedOutput
	command := exec.CommandContext(verificationCtx, executablePath, "version")
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("verify replacement executable: %w", ctx.Err())
		}
		if errors.Is(verificationCtx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("verify replacement executable: timed out after %s", timeout)
		}
		return fmt.Errorf("verify replacement executable: %w", err)
	}
	const prefix = "aria2s version "
	reported := strings.TrimSpace(output.String())
	if !strings.HasPrefix(reported, prefix) {
		return fmt.Errorf("replacement executable returned unexpected version output %q", reported)
	}
	actual, err := parseVersion(strings.TrimPrefix(reported, prefix))
	if err != nil || actual.compare(expected) != 0 {
		return fmt.Errorf("replacement executable reports %q, expected %s", strings.TrimPrefix(reported, prefix), expected.String())
	}
	return nil
}

type cappedOutput struct {
	data      []byte
	truncated bool
}

func (output *cappedOutput) Write(data []byte) (int, error) {
	written := len(data)
	remaining := maxCandidateOutput - len(output.data)
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		output.data = append(output.data, data...)
	}
	if written > remaining {
		output.truncated = true
	}
	return written, nil
}

func (output *cappedOutput) String() string {
	if output.truncated {
		return string(output.data) + "…"
	}
	return string(output.data)
}
