// Package aria2 owns the local aria2 configuration, JSON-RPC, and native
// session contracts. It preserves transport state without owning managed job
// intent or publication decisions.
package aria2

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/amio/aria2s/internal/atomicfile"
	"github.com/amio/aria2s/internal/state"
)

func DefaultConfig(downloadDir string) string {
	var builder strings.Builder
	writeLine(&builder, "dir", downloadDir)
	writeLine(&builder, "continue", "true")
	writeLine(&builder, "max-concurrent-downloads", "5")
	writeLine(&builder, "split", "8")
	writeLine(&builder, "max-connection-per-server", "8")
	writeLine(&builder, "min-split-size", "10M")
	// Keep restarts cheap: persist magnet metadata so reloaded magnets skip
	// peer metadata fetch, and trust saved progress so completed/seeding
	// torrents resume seeding without a full hash re-check (the .aria2
	// control file is deleted on completion, so aria2 would otherwise
	// re-verify the whole payload on every restart).
	writeLine(&builder, "bt-save-metadata", "true")
	writeLine(&builder, "bt-load-saved-metadata", "true")
	writeLine(&builder, "bt-seed-unverified", "true")
	return builder.String()
}

func WriteConfig(path, content string) error {
	return atomicfile.Create(path, []byte(content), 0o600)
}

func ParseConfig(content string) map[string]string {
	values := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return values
}

func ReadConfig(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	return ParseConfig(string(data)), nil
}

func ManagedArgs(current state.State) []string {
	return []string{
		"--enable-rpc=true",
		"--rpc-listen-all=false",
		"--rpc-listen-port=" + strconv.Itoa(current.RPCPort),
		"--rpc-secret=" + current.RPCSecret,
		"--input-file=" + current.SessionPath,
		"--save-session=" + current.SessionPath,
		"--save-session-interval=60",
	}
}

func ManagedV2Args(current state.State, hooksDir string) []string {
	return []string{
		"--enable-rpc=true",
		"--rpc-listen-all=false",
		"--rpc-listen-port=" + strconv.Itoa(current.RPCPort),
		"--rpc-secret=" + current.RPCSecret,
		"--input-file=" + current.StartupInputPath,
		"--save-session=" + current.SessionPath,
		"--save-session-interval=60",
		"--rpc-save-upload-metadata=false",
		"--on-download-complete=" + filepath.Join(hooksDir, "on-download-complete"),
		"--on-bt-download-complete=" + filepath.Join(hooksDir, "on-bt-download-complete"),
	}
}

func writeLine(builder *strings.Builder, key, value string) {
	builder.WriteString(key)
	builder.WriteByte('=')
	builder.WriteString(value)
	builder.WriteByte('\n')
}
