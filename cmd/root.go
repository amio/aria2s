package cmd

import (
	"fmt"

	"github.com/amio/aria2s/internal/app"
	"github.com/spf13/cobra"
)

func NewRoot(application *app.App) *cobra.Command {
	consoleCommand := newConsoleCommand(application)
	root := &cobra.Command{
		Use:          "aria2s",
		Short:        "Your aria2c, always on — sets it up as a background service with a TUI",
		SilenceUsage: true,
		RunE: func(command *cobra.Command, _ []string) error {
			return consoleCommand.RunE(command, nil)
		},
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
	root.AddCommand(consoleCommand)
	return root
}

func Execute() error {
	application, err := app.Default()
	if err != nil {
		return err
	}
	application.SetConsoleRunner(defaultConsoleRunner)
	return NewRoot(application).Execute()
}

func printErr(command *cobra.Command, format string, args ...any) {
	fmt.Fprintf(command.ErrOrStderr(), format, args...)
}
