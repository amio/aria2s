package paths

import "path/filepath"

const linuxServiceName = "aria2s.service"

func NewLinux(home string) Paths {
	configDir := filepath.Join(home, ".config", "aria2s")
	stateDir := filepath.Join(home, ".local", "state", "aria2s")
	return Paths{
		ServiceName:  linuxServiceName,
		ServiceFile:  filepath.Join(home, ".config", "systemd", "user", linuxServiceName),
		ConfigFile:   filepath.Join(configDir, "aria2.conf"),
		StateFile:    filepath.Join(stateDir, "state.json"),
		SessionFile:  filepath.Join(stateDir, "session"),
		LogFile:      filepath.Join(stateDir, "aria2.log"),
		ErrorLogFile: filepath.Join(stateDir, "aria2.err.log"),
	}
}
