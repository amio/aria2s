package cmd

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amio/aria2s/internal/app"
	"github.com/amio/aria2s/internal/tui"
	"github.com/spf13/cobra"
)

type dashboardRunner func(context.Context, *app.DashboardSession) error

func defaultDashboardRunner(ctx context.Context, session *app.DashboardSession) error {
	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	program := tea.NewProgram(tui.NewModel(sessionCtx, session, time.Second, Version), tea.WithContext(sessionCtx))
	_, err := program.Run()
	return err
}

func newDashboardCommand(application *app.App, runner dashboardRunner) *cobra.Command {
	return &cobra.Command{
		Use:   "dashboard",
		Short: "Open the interactive download dashboard",
		RunE: func(command *cobra.Command, _ []string) error {
			session, err := application.PrepareDashboard(command.Context())
			if err != nil {
				return err
			}
			return runner(command.Context(), session)
		},
	}
}
