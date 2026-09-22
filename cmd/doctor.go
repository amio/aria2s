package cmd

import (
	"fmt"
	"os"

	"github.com/amio/aria2s/internal/app"
	"github.com/amio/aria2s/internal/doctor"
	"github.com/amio/aria2s/internal/state"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

func newDoctorCommand(application *app.App) *cobra.Command {
	var repair, discardUnmanaged, noColor bool
	var output doctor.RenderOptions
	command := &cobra.Command{
		Use:   "doctor",
		Short: "Check installation, service health, and existing downloads",
		Long:  "Inspect installation, service/RPC health, and existing download tasks without changing them.\nReports include evidence and recovery suggestions. Errors or incomplete checks return a nonzero exit status; warnings alone do not.",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			output.Color = false
			output.Width = 0
			output.HomeDir, _ = os.UserHomeDir()
			if file, ok := command.OutOrStdout().(*os.File); ok {
				_, noColorEnv := os.LookupEnv("NO_COLOR")
				if term.IsTerminal(file.Fd()) {
					output.Width, _, _ = term.GetSize(file.Fd())
				}
				output.Color = term.IsTerminal(file.Fd()) && !noColor && !noColorEnv && os.Getenv("TERM") != "dumb"
			}
			if file, ok := command.ErrOrStderr().(*os.File); ok && term.IsTerminal(file.Fd()) {
				fmt.Fprintln(command.ErrOrStderr(), "Checking installation, service/RPC, and downloads...")
			}
			report := application.Doctor(command.Context())
			if repair {
				if report.Repair == nil {
					if err := doctor.Render(command.OutOrStdout(), report, output); err != nil {
						return err
					}
					fmt.Fprintln(command.ErrOrStderr(), "No supported automatic repair was found; follow the report's next steps.")
					return &reportedFailure{message: "doctor found no supported automatic repair"}
				}
				fmt.Fprintf(command.ErrOrStderr(), "Applying repair for %s...\n", report.Repair.Code)
				if err := application.RecoverRPC(command.Context(), discardUnmanaged); err != nil {
					current, _ := state.Load(application.Paths().StateFile)
					report.Checks = append(report.Checks, doctor.DiagnosticCheck{Group: doctor.Runtime, Name: "Repair", Issue: doctor.Issue{
						Code: "RepairFailed", Severity: doctor.Error, Summary: "automatic repair did not complete", Evidence: doctor.CleanText(err.Error(), current.RPCSecret),
						Recovery: []string{"Inspect `aria2s logs`, correct the reported condition, then rerun `aria2s doctor`."},
					}})
					if renderErr := doctor.Render(command.OutOrStdout(), report, output); renderErr != nil {
						return renderErr
					}
					return &reportedFailure{message: "doctor repair failed"}
				}
				fmt.Fprintln(command.ErrOrStderr(), "Repair verified: RPC is responding; managed download state was retained.")
				report = application.Doctor(command.Context())
			}
			if err := doctor.Render(command.OutOrStdout(), report, output); err != nil {
				return err
			}
			if !report.Healthy() || !report.Complete() {
				return &reportedFailure{message: "doctor reported failed or incomplete checks"}
			}
			return nil
		},
	}
	flags := command.Flags()
	flags.BoolVar(&repair, "repair", false, "apply a supported repair and verify the result")
	flags.BoolVar(&discardUnmanaged, "discard-unmanaged-tasks", false, "acknowledge that blocked RPC prevents preserving unmanaged tasks")
	flags.BoolVar(&output.JSON, "json", false, "emit one redacted JSON report (including failed checks)")
	flags.BoolVar(&output.Summary, "summary", false, "show grouped check rows without evidence or next steps")
	flags.BoolVar(&output.All, "all", false, "show every task and expand long text and suggestion lists")
	flags.BoolVar(&output.ASCII, "ascii", false, "use ASCII status labels")
	flags.BoolVar(&noColor, "no-color", false, "disable ANSI colors")
	command.MarkFlagsMutuallyExclusive("json", "summary")
	command.MarkFlagsMutuallyExclusive("json", "all")
	command.MarkFlagsMutuallyExclusive("summary", "all")
	command.PreRunE = func(command *cobra.Command, _ []string) error {
		if discardUnmanaged && !repair {
			return fmt.Errorf("--discard-unmanaged-tasks requires --repair")
		}
		return nil
	}
	return command
}
