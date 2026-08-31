package cli

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"aleutian-ai/ragctl/internal/registry"
)

func newRegistryCmd() *cobra.Command {
	registryCmd := &cobra.Command{
		Use:   "registry",
		Short: "Manage the knowledge registry",
	}

	registryCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List loaded knowledge-package manifests",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRegistryList(cmd)
		},
	})

	return registryCmd
}

func runRegistryList(cmd *cobra.Command) error {
	userDir, err := userRegistryDirPath()
	if err != nil {
		return fmt.Errorf("resolve user registry dir: %w", err)
	}

	reg, err := registry.NewLoader(userDir, "").Load(context.Background())
	if err != nil {
		return fmt.Errorf("load registry: %w", err)
	}

	out := cmd.OutOrStdout()
	for _, w := range reg.Warnings {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
	}

	names := reg.ManifestNames()
	sort.Strings(names)
	for _, name := range names {
		m, _ := reg.Manifest(name)
		fmt.Fprintf(out, "%-16s ecosystems=%v packages=%v\n", name, m.Match.Ecosystems, m.Match.Packages)
	}
	fmt.Fprintf(out, "\n%d manifests loaded\n", len(names))
	return nil
}
