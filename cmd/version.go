package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Version is the current aria2s version. Set at build time with ldflags.
var Version = "dev"

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print aria2s version",
		Run: func(command *cobra.Command, _ []string) {
			fmt.Fprintf(command.OutOrStdout(), "aria2s version %s\n", Version)
		},
	}
}
