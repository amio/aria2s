package cmd

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// Version is overridden by release builds through ldflags. Go-installed builds
// fall back to the module version embedded in their build metadata.
var Version = "dev"

func currentVersion() string {
	info, ok := debug.ReadBuildInfo()
	return resolveVersion(Version, info, ok)
}

func resolveVersion(linked string, info *debug.BuildInfo, ok bool) string {
	if linked != "dev" || !ok || info == nil || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return linked
	}
	return info.Main.Version
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print aria2s version",
		Run: func(command *cobra.Command, _ []string) {
			fmt.Fprintf(command.OutOrStdout(), "aria2s version %s\n", currentVersion())
		},
	}
}
