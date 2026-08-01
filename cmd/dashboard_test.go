package cmd

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
)

var _ term.File = (*terminalOutput)(nil)

type staticDashboardModel struct{}

func (staticDashboardModel) Init() tea.Cmd {
	return nil
}

func (model staticDashboardModel) Update(tea.Msg) (tea.Model, tea.Cmd) {
	return model, nil
}

func (staticDashboardModel) View() tea.View {
	return tea.NewView("dashboard")
}

func TestTerminalOutputFailureStopsRenderer(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })

	ctx, cancel := context.WithCancel(t.Context())
	output := newTerminalOutput(writer, cancel)
	program := tea.NewProgram(
		staticDashboardModel{},
		tea.WithContext(ctx),
		tea.WithInput(nil),
		tea.WithOutput(output),
		tea.WithWindowSize(80, 24),
		tea.WithFPS(120),
	)

	done := make(chan error, 1)
	go func() {
		_, runErr := program.Run()
		done <- runErr
	}()

	select {
	case runErr := <-done:
		if !errors.Is(runErr, context.Canceled) {
			t.Fatalf("expected renderer failure to cancel program, got %v", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("renderer did not stop after terminal output failure")
	}
	if output.Err() == nil {
		t.Fatal("expected terminal output failure to be retained")
	}
}
