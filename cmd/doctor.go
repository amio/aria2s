package cmd

import (
	"fmt"

	"github.com/amio/aria2s/internal/app"
	"github.com/spf13/cobra"
)

func newDoctorCommand(application *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose service configuration",
		RunE: func(command *cobra.Command, _ []string) error {
			report := application.Doctor(command.Context())
			if report.Healthy {
				fmt.Fprintln(command.OutOrStdout(), "aria2s doctor: healthy")
				return nil
			}
			fmt.Fprintln(command.OutOrStdout(), "aria2s doctor: issues found")
			for _, issue := range report.Issues {
				fmt.Fprintf(command.OutOrStdout(), "- %s\n", issue.Message)
			}
			return fmt.Errorf("doctor reported issues")
		},
	}
}
