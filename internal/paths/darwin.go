package paths

import "path/filepath"

const serviceName = "io.github.amio.aria2s"

func NewDarwin(home string) Paths {
	configDir := filepath.Join(home, "Library", "Application Support", "aria2s")
	logDir := filepath.Join(home, "Library", "Logs", "aria2s")
	return Paths{
		StateDir:          configDir,
		ServiceName:       serviceName,
		ServiceFile:       filepath.Join(home, "Library", "LaunchAgents", serviceName+".plist"),
		ConfigFile:        filepath.Join(home, ".aria2", "aria2.conf"),
		StateFile:         filepath.Join(configDir, "state.json"),
		SessionFile:       filepath.Join(configDir, "runtime-v2.session"),
		LegacySessionFile: filepath.Join(configDir, "session"),
		StartupInputFile:  filepath.Join(configDir, "startup.input"),
		JobsDir:           filepath.Join(configDir, "jobs"),
		StoragesDir:       filepath.Join(configDir, "storages"),
		HooksDir:          filepath.Join(configDir, "hooks"),
		InstanceLockFile:  filepath.Join(configDir, "instance.lock"),
		SafeStartupFile:   filepath.Join(configDir, "safe-startup"),
		LogFile:           filepath.Join(logDir, "aria2.log"),
		ErrorLogFile:      filepath.Join(logDir, "aria2.err.log"),
	}
}
