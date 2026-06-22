package cmd

import (
	"fmt"

	"github.com/amio/aria2s/internal/app"
	"github.com/spf13/cobra"
)

func newRestartCommand(application *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "Restart the aria2 service",
		RunE: func(command *cobra.Command, _ []string) error {
			if err := application.Restart(command.Context()); err != nil {
				return err
			}
			fmt.Fprintln(command.OutOrStdout(), "aria2s restarted.")
			return nil
		},
	}
}
