package state

import (
	"encoding/json"
	"os"
	"path/filepath"
)

/** State is the authoritative local runtime metadata for aria2s-managed RPC access. */
type State struct {
	Aria2cPath   string   `json:"aria2cPath"`
	RPCPort      int      `json:"rpcPort"`
	RPCSecret    string   `json:"rpcSecret"`
	SessionPath  string   `json:"sessionPath"`
	LogPath      string   `json:"logPath"`
	ErrorLogPath string   `json:"errorLogPath"`
	ServiceName  string   `json:"serviceName"`
	RecentDirs   []string `json:"recentDirs,omitempty"`
}

func Save(path string, current State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".state-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
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
	return os.Rename(tempPath, path)
}

func Load(path string) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	var current State
	if err := json.Unmarshal(data, &current); err != nil {
		return State{}, err
	}
	return current, nil
}
