package cmd

import (
	"fmt"

	"github.com/amio/aria2s/internal/app"
	"github.com/spf13/cobra"
)

func NewRoot(application *app.App) *cobra.Command {
	root := &cobra.Command{
		Use:          "asv",
		Short:        "Manage a local aria2 background service",
		SilenceUsage: true,
	}
	root.AddCommand(newInstallCommand(application))
	root.AddCommand(newUninstallCommand(application))
	root.AddCommand(newStartCommand(application))
	root.AddCommand(newStopCommand(application))
	root.AddCommand(newRestartCommand(application))
	root.AddCommand(newStatusCommand(application))
	root.AddCommand(newLogsCommand(application))
	root.AddCommand(newDoctorCommand(application))
	root.AddCommand(newAddCommand(application))
	return root
}

func Execute() error {
	application, err := app.Default()
	if err != nil {
		return err
	}
	return NewRoot(application).Execute()
}

func printErr(command *cobra.Command, format string, args ...any) {
	fmt.Fprintf(command.ErrOrStderr(), format, args...)
}
