package aria2

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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

func ManagedArgs(current state.State) []string {
	return []string{
		"--enable-rpc=true",
		"--rpc-listen-all=false",
		"--rpc-listen-port=" + strconv.Itoa(current.RPCPort),
		"--rpc-secret=" + current.RPCSecret,
		"--input-file=" + current.SessionPath,
		"--save-session=" + current.SessionPath,
		"--force-save=true",
		"--save-session-interval=60",
	}
}

func writeLine(builder *strings.Builder, key, value string) {
	builder.WriteString(key)
	builder.WriteByte('=')
	builder.WriteString(value)
	builder.WriteByte('\n')
}
