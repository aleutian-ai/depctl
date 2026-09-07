// Package cli wires the ragctl command tree.
package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// NewRootCmd builds the root ragctl command with every top-level
// subcommand registered. Unimplemented subcommands fail loudly with a
// fixed error rather than silently doing nothing.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "ragctl",
		Short:         "Dependency-aware knowledge synchronization for AI coding agents",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	root.AddCommand(
		newInitCmd(),
		newScanCmd(),
		newProjectCmd(),
		newDepsCmd(),
		newDescribeCmd(),
		newPlanCmd(),
		newSyncCmd(),
		notImplementedCmd("status", "Show system status"),
		notImplementedCmd("doctor", "Run health diagnostics"),
		newGCCmd(),
		notImplementedCmd("watch", "Watch projects for dependency changes"),
		newServeCmd(),
		notImplementedCmd("backend", "Manage vector backends"),
		newRegistryCmd(),
		newConfigCmd(),
	)

	return root
}

// notImplementedCmd returns a stub command whose RunE always fails with a
// fixed, greppable error string rather than silently no-op'ing.
func notImplementedCmd(use, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("feature not implemented in this build")
		},
	}
}
