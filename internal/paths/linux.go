package paths

import "path/filepath"

const linuxServiceName = "aria2s.service"

func NewLinux(home string) Paths {
	stateDir := filepath.Join(home, ".local", "state", "aria2s")
	return Paths{
		StateDir:          stateDir,
		ServiceName:       linuxServiceName,
		ServiceFile:       filepath.Join(home, ".config", "systemd", "user", linuxServiceName),
		ConfigFile:        filepath.Join(home, ".aria2", "aria2.conf"),
		StateFile:         filepath.Join(stateDir, "state.json"),
		SessionFile:       filepath.Join(stateDir, "runtime-v2.session"),
		LegacySessionFile: filepath.Join(stateDir, "session"),
		StartupInputFile:  filepath.Join(stateDir, "startup.input"),
		JobsDir:           filepath.Join(stateDir, "jobs"),
		StoragesDir:       filepath.Join(stateDir, "storages"),
		HooksDir:          filepath.Join(stateDir, "hooks"),
		InstanceLockFile:  filepath.Join(stateDir, "instance.lock"),
		LogFile:           filepath.Join(stateDir, "aria2.log"),
		ErrorLogFile:      filepath.Join(stateDir, "aria2.err.log"),
	}
}
