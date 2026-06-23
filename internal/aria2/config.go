package aria2

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/amio/aria2s/internal/state"
)

/** ManagedConfig contains aria2.conf values that aria2s owns and repairs. */
type ManagedConfig struct {
	RPCPort     int
	RPCSecret   string
	SessionFile string
	DownloadDir string
}

var managedConfigKeys = map[string]struct{}{
	"enable-rpc":            {},
	"rpc-listen-all":        {},
	"rpc-listen-port":       {},
	"rpc-secret":            {},
	"input-file":            {},
	"save-session":          {},
	"force-save":            {},
	"save-session-interval": {},
}

func BuildConfig(managed ManagedConfig, current map[string]string) string {
	values := map[string]string{
		"dir":                       managed.DownloadDir,
		"continue":                  "true",
		"max-concurrent-downloads":  "5",
		"split":                     "8",
		"max-connection-per-server": "8",
		"min-split-size":            "10M",
	}
	for key, value := range current {
		if _, isManaged := managedConfigKeys[key]; !isManaged {
			values[key] = value
		}
	}

	var builder strings.Builder
	writeLine(&builder, "enable-rpc", "true")
	writeLine(&builder, "rpc-listen-all", "false")
	writeLine(&builder, "rpc-listen-port", strconv.Itoa(managed.RPCPort))
	writeLine(&builder, "rpc-secret", managed.RPCSecret)
	builder.WriteByte('\n')
	writeLine(&builder, "input-file", managed.SessionFile)
	writeLine(&builder, "save-session", managed.SessionFile)
	writeLine(&builder, "force-save", "true")
	writeLine(&builder, "save-session-interval", "60")
	builder.WriteByte('\n')

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		writeLine(&builder, key, values[key])
	}
	return builder.String()
}

func WriteConfig(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
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

func ManagedValues(current state.State) map[string]string {
	return map[string]string{
		"enable-rpc":            "true",
		"rpc-listen-all":        "false",
		"rpc-listen-port":       fmt.Sprintf("%d", current.RPCPort),
		"rpc-secret":            current.RPCSecret,
		"input-file":            current.SessionPath,
		"save-session":          current.SessionPath,
		"force-save":            "true",
		"save-session-interval": "60",
	}
}

func HasManagedDrift(values map[string]string, current state.State) bool {
	for key, want := range ManagedValues(current) {
		if values[key] != want {
			return true
		}
	}
	return false
}

func writeLine(builder *strings.Builder, key, value string) {
	builder.WriteString(key)
	builder.WriteByte('=')
	builder.WriteString(value)
	builder.WriteByte('\n')
}

