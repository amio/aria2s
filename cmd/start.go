package cmd

import (
	"fmt"

	"github.com/amio/aria2s/internal/app"
	"github.com/spf13/cobra"
)

func newStartCommand(application *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the aria2 service",
		RunE: func(command *cobra.Command, _ []string) error {
			if err := application.Start(command.Context()); err != nil {
				return err
			}
			fmt.Fprintln(command.OutOrStdout(), "aria2s started.")
			return nil
		},
	}
}
