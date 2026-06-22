package cmd

import (
	"fmt"

	"github.com/amio/aria2s/internal/app"
	"github.com/amio/aria2s/internal/aria2"
	"github.com/spf13/cobra"
)

func newAddCommand(application *app.App) *cobra.Command {
	var dir string
	command := &cobra.Command{
		Use:   "add <url-or-magnet>",
		Short: "Add a download URL or magnet URI",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			gid, err := application.Add(command.Context(), args[0], aria2.AddOptions{Dir: dir})
			if err != nil {
				return err
			}
			fmt.Fprintf(command.OutOrStdout(), "Added download.\n\nGID:\n  %s\n", gid)
			return nil
		},
	}
	command.Flags().StringVarP(&dir, "dir", "d", "", "download directory override")
	return command
}
