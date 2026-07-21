package cmd

import (
	"github.com/amio/aria2s/internal/app"
	"github.com/spf13/cobra"
)

// newCompleteHookCommand is the hidden entrypoint aria2c invokes via the
// generated on-download-complete launcher script. aria2 appends
// <gid> <num-files> <path> to the hook command; only the gid is needed —
// everything else is read back over RPC.
func newCompleteHookCommand(application *app.App) *cobra.Command {
	return &cobra.Command{
		Use:    "complete-hook <gid> [num-files] [path]",
		Short:  "Internal: handle aria2's on-download-complete event",
		Hidden: true,
		Args:   cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return application.CompleteStagedDownload(command.Context(), args[0])
		},
	}
}
