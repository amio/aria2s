package cmd

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amio/aria2s/internal/app"
	"github.com/amio/aria2s/internal/tui"
	"github.com/spf13/cobra"
)

func defaultDashboardRunner(application *app.App) error {
	program := tea.NewProgram(tui.NewModel(application, time.Second, Version))
	_, err := program.Run()
	return err
}

func newDashboardCommand(application *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "dashboard",
		Short: "Open the interactive download dashboard",
		RunE: func(command *cobra.Command, _ []string) error {
			if err := application.EnsureDashboardReady(command.Context()); err != nil {
				return err
			}
			return application.RunDashboard()
		},
	}
}
