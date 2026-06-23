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
