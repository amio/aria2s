package cmd

import (
	"fmt"
	"strings"

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
				fmt.Fprintf(command.OutOrStdout(), "- [%s] %s: %s\n", issue.Severity, issue.Code, issue.Summary)
				if issue.Evidence != "" {
					fmt.Fprintf(command.OutOrStdout(), "  evidence: %s\n", issue.Evidence)
				}
				if len(issue.Recovery) > 0 {
					fmt.Fprintf(command.OutOrStdout(), "  recovery: %s\n", strings.Join(issue.Recovery, " "))
				}
			}
			return fmt.Errorf("doctor reported issues")
		},
	}
}
