// Package state persists the committed managed-runtime identity. Schema v2 is
// activated only after its controller, service artifact, and versioned session
// locations are prepared and can be validated at process startup.
package state

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/amio/aria2s/internal/atomicfile"
)

/** State is the authoritative local runtime metadata for aria2s-managed RPC access. */
type State struct {
	RuntimeSchemaVersion int      `json:"runtimeSchemaVersion"`
	ControllerPath       string   `json:"controllerPath"`
	ControllerIdentity   string   `json:"controllerIdentity"`
	ServiceIdentity      string   `json:"serviceIdentity"`
	Aria2cPath           string   `json:"aria2cPath"`
	RPCPort              int      `json:"rpcPort"`
	RPCSecret            string   `json:"rpcSecret"`
	SessionPath          string   `json:"sessionPath"`
	StartupInputPath     string   `json:"startupInputPath"`
	LogPath              string   `json:"logPath"`
	ErrorLogPath         string   `json:"errorLogPath"`
	ServiceName          string   `json:"serviceName"`
	RecentDirs           []string `json:"recentDirs,omitempty"`
}

func Save(path string, current State) error {
	data, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, data, 0o600)
}

func Load(path string) (State, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return State{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return State{}, errors.New("runtime state is not a regular file")
	}
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
