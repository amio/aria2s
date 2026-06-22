package cmd

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/amio/aria2s/internal/app"
	"github.com/amio/aria2s/internal/tui"
	"github.com/spf13/cobra"
)

func newConsoleCommand(application *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "console",
		Short: "Open the interactive aria2 console",
		RunE: func(command *cobra.Command, _ []string) error {
			program := tea.NewProgram(tui.NewModel(application, time.Second))
			_, err := program.Run()
			return err
		},
	}
}
