package cmd

import (
	"fmt"
	"strings"

	"github.com/amio/aria2s/internal/app"
	"github.com/amio/aria2s/internal/doctor"
	"github.com/spf13/cobra"
)

func newDoctorCommand(application *app.App) *cobra.Command {
	var repair bool
	var discardUnmanaged bool
	command := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose service configuration",
		RunE: func(command *cobra.Command, _ []string) error {
			report := application.Doctor(command.Context())
			if repair {
				if report.Repair == nil {
					renderDoctorReport(command, report)
					return &reportedFailure{message: "doctor found no supported automatic repair"}
				}
				fmt.Fprintf(command.OutOrStdout(), "Applying repair for %s...\n", report.Repair.Code)
				if err := application.RecoverRPC(command.Context(), discardUnmanaged); err != nil {
					return err
				}
				fmt.Fprintln(command.OutOrStdout(), "✓ Repair: RPC is responding; managed download state was retained.")
				fmt.Fprintln(command.OutOrStdout())
				report = application.Doctor(command.Context())
			}
			renderDoctorReport(command, report)
			if !report.Healthy {
				return &reportedFailure{message: "doctor reported failed checks"}
			}
			return nil
		},
	}
	command.Flags().BoolVar(&repair, "repair", false, "apply a supported repair and verify the result")
	command.Flags().BoolVar(&discardUnmanaged, "discard-unmanaged-tasks", false, "acknowledge that blocked RPC prevents preserving unmanaged tasks")
	return command
}

func renderDoctorReport(command *cobra.Command, report doctor.Report) {
	fmt.Fprintln(command.OutOrStdout(), "aria2s doctor")
	for _, check := range report.Checks {
		mark := "✓"
		if !check.Healthy {
			mark = "✗"
			if check.Severity == "warning" {
				mark = "!"
			}
		}
		fmt.Fprintf(command.OutOrStdout(), "%s %s: %s\n", mark, check.Name, check.Summary)
		if check.Evidence != "" {
			fmt.Fprintf(command.OutOrStdout(), "  Evidence: %s\n", check.Evidence)
		}
		if len(check.Recovery) > 0 && (report.Repair == nil || check.Name != "RPC" && check.Name != "Startup") {
			fmt.Fprintf(command.OutOrStdout(), "  Fix: %s\n", strings.Join(check.Recovery, " "))
		}
	}
	if report.Healthy {
		fmt.Fprintln(command.OutOrStdout(), "\nNo issues found.")
		return
	}
	if report.Repair != nil {
		fmt.Fprintln(command.OutOrStdout(), "\nRecommended repair:")
		fmt.Fprintf(command.OutOrStdout(), "  %s\n", report.Repair.Command)
		fmt.Fprintf(command.OutOrStdout(), "  %s\n", report.Repair.Summary)
	}
}
