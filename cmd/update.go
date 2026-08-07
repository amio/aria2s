package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/amio/aria2s/internal/upgrade"
	"github.com/spf13/cobra"
)

type updateWorkflow func(context.Context, upgrade.Options) (upgrade.Result, error)
type privilegeRunner func(context.Context, string, io.Reader, io.Writer, io.Writer) error
type controllerRebinder func(context.Context) (bool, error)

func newUpdateCommand(rebind controllerRebinder) *cobra.Command {
	return newUpdateCommandWith(upgrade.Run, rebind, runUpdateWithSudo, os.Geteuid)
}

func newUpdateCommandWith(workflow updateWorkflow, rebind controllerRebinder, elevate privilegeRunner, effectiveUID func() int) *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update aria2s to the latest release",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			result, err := workflow(command.Context(), upgrade.Options{CurrentVersion: Version})
			var privilegeFailure *upgrade.PrivilegeError
			if errors.As(err, &privilegeFailure) && effectiveUID() != 0 {
				fmt.Fprintln(command.OutOrStdout(), "Administrator permission is required; retrying with sudo.")
				if err := elevate(command.Context(), privilegeFailure.ExecutablePath, command.InOrStdin(), command.OutOrStdout(), command.ErrOrStderr()); err != nil {
					return fmt.Errorf("update with sudo: %w", err)
				}
				return rebindController(command, rebind, true)
			}
			if err != nil {
				return err
			}
			if result.Updated {
				fmt.Fprintf(command.OutOrStdout(), "Updated aria2s %s → %s.\n", result.Current, result.Latest)
				return rebindController(command, rebind, true)
			}
			if err := rebindController(command, rebind, false); err != nil {
				return err
			}
			fmt.Fprintf(command.OutOrStdout(), "aria2s is already up to date (%s).\n", result.Current)
			return nil
		},
	}
}

func newUpdateReplaceCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "update-replace",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			result, err := upgrade.Run(command.Context(), upgrade.Options{CurrentVersion: Version})
			if err != nil {
				return err
			}
			if result.Updated {
				fmt.Fprintf(command.OutOrStdout(), "Updated aria2s %s → %s.\n", result.Current, result.Latest)
			} else {
				fmt.Fprintf(command.OutOrStdout(), "aria2s is already up to date (%s).\n", result.Current)
			}
			return nil
		},
	}
}

func rebindController(command *cobra.Command, rebind controllerRebinder, binaryChanged bool) error {
	bound, err := rebind(command.Context())
	if err != nil {
		if binaryChanged {
			return fmt.Errorf("aria2s was updated, but managed service metadata could not be refreshed; run `aria2s install`: %w", err)
		}
		return fmt.Errorf("refresh managed service metadata: %w", err)
	}
	if bound {
		fmt.Fprintln(command.OutOrStdout(), "Managed service metadata was refreshed; running downloads were not restarted.")
	}
	return nil
}

func runUpdateWithSudo(ctx context.Context, executablePath string, stdin io.Reader, stdout, stderr io.Writer) error {
	command := exec.CommandContext(ctx, "sudo", "--", executablePath, "update-replace")
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}
