package cmd

import (
	"fmt"

	"github.com/amio/aria2s/internal/app"
	"github.com/spf13/cobra"
)

func newStatusCommand(application *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show service health",
		RunE: func(command *cobra.Command, _ []string) error {
			fmt.Fprint(command.OutOrStdout(), application.Status(command.Context()).String())
			return nil
		},
	}
}
