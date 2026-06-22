package cmd

import (
	"fmt"
	"os"

	"github.com/amio/aria2s/internal/app"
	"github.com/spf13/cobra"
)

func newLogsCommand(application *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "logs",
		Short: "Show log file paths",
		RunE: func(command *cobra.Command, _ []string) error {
			paths := application.Paths()
			fmt.Fprintf(command.OutOrStdout(), "Logs:\n  %s\n  %s\n\n", paths.LogFile, paths.ErrorLogFile)
			printRecentLog(command, "stdout", paths.LogFile)
			printRecentLog(command, "stderr", paths.ErrorLogFile)
			return nil
		},
	}
}

func printRecentLog(command *cobra.Command, label, path string) {
	fmt.Fprintf(command.OutOrStdout(), "%s:\n", label)
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(command.OutOrStdout(), "  unavailable: %v\n\n", err)
		return
	}
	const maxBytes = 4096
	if len(data) > maxBytes {
		data = data[len(data)-maxBytes:]
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
