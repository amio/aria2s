package cmd

import (
	"fmt"

	"github.com/amio/aria2s/internal/app"
	"github.com/spf13/cobra"
)

func newRestartCommand(application *app.App) *cobra.Command {
	var discardUnmanaged bool
	command := &cobra.Command{
		Use:   "restart",
		Short: "Restart the aria2 service",
		RunE: func(command *cobra.Command, _ []string) error {
			if err := application.RestartManaged(command.Context(), app.StopOptions{DiscardUnmanagedTasks: discardUnmanaged}); err != nil {
				return err
			}
			fmt.Fprintln(command.OutOrStdout(), "aria2s restarted.")
			return nil
		},
	}
	command.Flags().BoolVar(&discardUnmanaged, "discard-unmanaged-tasks", false, "restart even though unmanaged tasks cannot be restored")
	return command
}
