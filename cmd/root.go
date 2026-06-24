package cmd

import (
	"fmt"
	"strings"

	"github.com/amio/aria2s/internal/app"
	"github.com/spf13/cobra"
)

func NewRoot(application *app.App) *cobra.Command {
	dashboardCommand := newDashboardCommand(application)
	root := &cobra.Command{
		Use:                   "aria2s",
		Short:                 "Your aria2c, always on — sets it up as a background service, manage downloads with a terminal dashboard",
		SilenceUsage:          true,
		DisableFlagsInUseLine: true,
		RunE: func(command *cobra.Command, _ []string) error {
			return dashboardCommand.RunE(command, nil)
		},
	}

	// Command groups for better help readability.
	root.AddGroup(&cobra.Group{ID: "downloads", Title: "Download Management"})
	root.AddGroup(&cobra.Group{ID: "service", Title: "Service Control"})
	root.AddGroup(&cobra.Group{ID: "setup", Title: "Setup & Maintenance"})

	// Download Management
	dashboardCmd := dashboardCommand
	dashboardCmd.GroupID = "downloads"
	root.AddCommand(dashboardCmd)

	addCmd := newAddCommand(application)
	addCmd.GroupID = "downloads"
	root.AddCommand(addCmd)

	// Service Control
	startCmd := newStartCommand(application)
	startCmd.GroupID = "service"
	root.AddCommand(startCmd)

	stopCmd := newStopCommand(application)
	stopCmd.GroupID = "service"
	root.AddCommand(stopCmd)

	restartCmd := newRestartCommand(application)
	restartCmd.GroupID = "service"
	root.AddCommand(restartCmd)

	statusCmd := newStatusCommand(application)
	statusCmd.GroupID = "service"
	root.AddCommand(statusCmd)

	// Setup & Maintenance
	installCmd := newInstallCommand(application)
	installCmd.GroupID = "setup"
	root.AddCommand(installCmd)

	uninstallCmd := newUninstallCommand(application)
	uninstallCmd.GroupID = "setup"
	root.AddCommand(uninstallCmd)

	logsCmd := newLogsCommand(application)
	logsCmd.GroupID = "setup"
	root.AddCommand(logsCmd)

	doctorCmd := newDoctorCommand(application)
	doctorCmd.GroupID = "setup"
	root.AddCommand(doctorCmd)

	root.SetHelpFunc(customRootHelp)
	return root
}

func Execute() error {
	application, err := app.Default()
	if err != nil {
		return err
	}
	application.SetDashboardRunner(defaultDashboardRunner)
	return NewRoot(application).Execute()
}

func printErr(command *cobra.Command, format string, args ...any) {
	fmt.Fprintf(command.ErrOrStderr(), format, args...)
}

func customRootHelp(cmd *cobra.Command, _ []string) {
	w := cmd.OutOrStdout()

	// Header description.
	if cmd.Short != "" {
		fmt.Fprintln(w, cmd.Short)
		fmt.Fprintln(w)
	}

	// Usage with inline comments.
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintf(w, "  \033[1m%s\033[0m             Ensure setup/start, then open the dashboard\n", cmd.CommandPath())
	fmt.Fprintf(w, "  \033[1m%s\033[0m [command]   Run a management command\n", cmd.CommandPath())

	// Collect grouped commands and find max name length for alignment.
	type groupEntry struct {
		group *cobra.Group
		cmds  []*cobra.Command
	}
	var entries []groupEntry
	maxLen := 0

	for _, group := range cmd.Groups() {
		var groupCmds []*cobra.Command
		for _, sub := range cmd.Commands() {
			if sub.GroupID == group.ID && sub.IsAvailableCommand() && sub.Name() != "help" {
				groupCmds = append(groupCmds, sub)
				if l := len(sub.Name()); l > maxLen {
					maxLen = l
				}
			}
		}
		if len(groupCmds) > 0 {
			entries = append(entries, groupEntry{group: group, cmds: groupCmds})
		}
	}

	const groupIndent = "  "
	const cmdIndent = groupIndent + "  " // two levels of indentation
	padTo := maxLen + 8                   // spacing from end of name to description

	if len(entries) > 0 {
		fmt.Fprintln(w, "\nCommands:")
	}
	for i, entry := range entries {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s%s\n", groupIndent, entry.group.Title)
		for _, sub := range entry.cmds {
			boldName := fmt.Sprintf("\033[1m%s\033[0m", sub.Name())
			pad := padTo - len(sub.Name())
			if pad < 0 {
				pad = 0
			}
			fmt.Fprintf(w, "%s%s%s%s\n", cmdIndent, boldName, strings.Repeat(" ", pad), sub.Short)
		}
	}

	// Flags (--help).
	if cmd.HasAvailableLocalFlags() {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Flags:")
		fmt.Fprint(w, cmd.LocalFlags().FlagUsages())
	}

	// Footer.
	if cmd.HasAvailableSubCommands() {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "Use \"%s [command] --help\" for more information about a command.\n", cmd.CommandPath())
	}
}
