package cmd

import (
	"fmt"

	"github.com/amio/aria2s/internal/app"
	"github.com/spf13/cobra"
)

func newAddCommand(application *app.App) *cobra.Command {
	var dir string
	command := &cobra.Command{
		Use:   "add <url-or-magnet>",
		Short: "Add a download URL or magnet URI",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			result, err := application.AddManaged(command.Context(), app.AddRequest{Source: args[0], TargetDir: dir})
			if err != nil {
				return err
			}
			fmt.Fprintf(command.OutOrStdout(), "Added managed download.\n\nJob ID:\n  %s\n", result.Task.JobID)
			if result.Warning != nil {
				fmt.Fprintf(command.ErrOrStderr(), "warning: %v\n", result.Warning)
			}
			return nil
		},
	}
	command.Flags().StringVarP(&dir, "dir", "d", "", "download directory override")
	return command
}
