package cmd

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amio/aria2s/internal/app"
	"github.com/amio/aria2s/internal/tui"
	"github.com/spf13/cobra"
)

type dashboardRunner func(context.Context, *app.DashboardSession) error

type terminalOutput struct {
	file   *os.File
	cancel context.CancelFunc

	mu  sync.Mutex
	err error
}

func newTerminalOutput(file *os.File, cancel context.CancelFunc) *terminalOutput {
	return &terminalOutput{file: file, cancel: cancel}
}

func (output *terminalOutput) Read(data []byte) (int, error) {
	return output.file.Read(data)
}

func (output *terminalOutput) Write(data []byte) (int, error) {
	written, err := output.file.Write(data)
	if err == nil {
		return written, nil
	}

	output.mu.Lock()
	firstFailure := output.err == nil
	if firstFailure {
		output.err = err
	}
	output.mu.Unlock()
	if firstFailure {
		// Bubble Tea v2.0.8 drops renderer flush errors. Cancelling here prevents
		// its frame ticker from repeatedly redrawing after the terminal is revoked.
		output.cancel()
	}
	return written, err
}

func (output *terminalOutput) Fd() uintptr {
	return output.file.Fd()
}

// Close is intentionally a no-op because terminalOutput observes stdout but
// does not own it. The method completes Bubble Tea's TTY file contract.
func (output *terminalOutput) Close() error {
	return nil
}

func (output *terminalOutput) Err() error {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.err
}

func defaultDashboardRunner(ctx context.Context, session *app.DashboardSession) error {
	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	output := newTerminalOutput(os.Stdout, cancel)
	program := tea.NewProgram(
		tui.NewModel(sessionCtx, session, time.Second, currentVersion()),
		tea.WithContext(sessionCtx),
		tea.WithOutput(output),
	)
	_, err := program.Run()
	if outputErr := output.Err(); outputErr != nil {
		return fmt.Errorf("dashboard terminal output failed: %w", outputErr)
	}
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
