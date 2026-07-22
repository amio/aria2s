package cmd

import (
	"fmt"

	"github.com/amio/aria2s/internal/app"
	"github.com/spf13/cobra"
)

func newInstallCommand(application *app.App) *cobra.Command {
	var start bool
	var discardLegacyTasks bool
	command := &cobra.Command{
		Use:   "install",
		Short: "Install the background service",
		RunE: func(command *cobra.Command, _ []string) error {
			if err := application.InstallManaged(command.Context(), app.InstallRequest{Start: start, DiscardLegacyTasks: discardLegacyTasks}); err != nil {
				return err
			}
			paths := application.Paths()
			if start {
				fmt.Fprintln(command.OutOrStdout(), "aria2s installed and started.")
			} else {
				fmt.Fprintln(command.OutOrStdout(), "aria2s installed.")
			}
			fmt.Fprintf(command.OutOrStdout(), "\nService:\n  %s\n\nConfig:\n  %s\n\nLogs:\n  %s\n", paths.ServiceName, paths.ConfigFile, paths.LogFile)
			return nil
		},
	}
	command.Flags().BoolVar(&start, "start", false, "start the service after installing")
	command.Flags().BoolVar(&discardLegacyTasks, "discard-legacy-tasks", false, "ignore legacy restart state without deleting legacy files")
	return command
}
