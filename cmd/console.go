package cmd

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/amio/aria2s/internal/app"
	"github.com/amio/aria2s/internal/tui"
	"github.com/spf13/cobra"
)

var runConsole = func(application *app.App) error {
	program := tea.NewProgram(tui.NewModel(application, time.Second, Version), tea.WithAltScreen())
	_, err := program.Run()
	return err
}

func newConsoleCommand(application *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "console",
		Short: "Open the interactive aria2 console",
		RunE: func(command *cobra.Command, _ []string) error {
			if err := application.EnsureConsoleReady(command.Context()); err != nil {
				return err
			}
			return runConsole(application)
		},
	}
}
