// Package service renders and controls platform supervisors. It preserves
// argv parity across launchd and systemd while leaving lifecycle policy to app.
package service

import "context"

type Backend interface {
	Install(context.Context) error
	Uninstall(context.Context) error
	Start(context.Context) error
	Stop(context.Context) error
	IsLoaded(context.Context) bool
	IsRunning(context.Context) bool
}

type CommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

// MaxOpenFiles is the NOFILE rlimit applied to the managed aria2c process.
// launchd agents do not inherit the interactive shell's raised ulimit, so
// without an explicit rlimit the daemon is capped at the system default
// (256 on stock macOS), which BT peer + multi-split HTTP downloads exhaust.
const MaxOpenFiles = 65536
