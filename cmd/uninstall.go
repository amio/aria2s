package cmd

import (
	"fmt"

	"github.com/amio/aria2s/internal/app"
	"github.com/spf13/cobra"
)

func newUninstallCommand(application *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall the LaunchAgent",
		RunE: func(command *cobra.Command, _ []string) error {
			if err := application.Uninstall(command.Context()); err != nil {
				return err
			}
			fmt.Fprintln(command.OutOrStdout(), "aria2s uninstalled.")
			return nil
		},
	}
}
