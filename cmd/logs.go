package cmd

import (
	"fmt"

	"github.com/amio/aria2s/internal/app"
	managedruntime "github.com/amio/aria2s/internal/runtime"
	"github.com/spf13/cobra"
)

func newLogsCommand(application *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "logs",
		Short: "Show log file paths",
		RunE: func(command *cobra.Command, _ []string) error {
			paths := application.Paths()
			fmt.Fprintf(command.OutOrStdout(), "Logs (50 MiB startup rotation; current + .1 + .2):\n  %s\n  %s\n\n", paths.LogFile, paths.ErrorLogFile)
			printRecentLog(command, "stdout", paths.LogFile)
			printRecentLog(command, "stderr", paths.ErrorLogFile)
			return nil
		},
	}
}

func printRecentLog(command *cobra.Command, label, path string) {
	fmt.Fprintf(command.OutOrStdout(), "%s:\n", label)
	data, err := managedruntime.ReadLogTail(path, 4096)
	if err != nil {
		fmt.Fprintf(command.OutOrStdout(), "  unavailable: %v\n\n", err)
		return
	}
	if len(data) == 0 {
		fmt.Fprintln(command.OutOrStdout(), "  <empty>")
	} else {
		fmt.Fprint(command.OutOrStdout(), string(data))
		if data[len(data)-1] != '\n' {
			fmt.Fprintln(command.OutOrStdout())
		}
	}
	fmt.Fprintln(command.OutOrStdout())
}
