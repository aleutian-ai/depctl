package cli

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/registry"
	"aleutian-ai/ragctl/internal/registry/discover"
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

	registryCmd.AddCommand(&cobra.Command{
		Use:   "discover <ecosystem> <package>",
		Short: "Propose candidate knowledge sources from the package's own ecosystem metadata (draft only, never applied)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRegistryDiscover(cmd, domain.Ecosystem(args[0]), args[1])
		},
	})

	return registryCmd
}

func runRegistryDiscover(cmd *cobra.Command, ecosystem domain.Ecosystem, pkg string) error {
	sources, err := discover.Discover(context.Background(), ecosystem, pkg)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "no candidate sources found in %s metadata for %s\n", ecosystem, pkg)
		return nil
	}

	draft := registry.Manifest{
		APIVersion: "ragctl.dev/v1alpha1",
		Kind:       "KnowledgePackage",
		Metadata:   registry.Metadata{Name: pkg},
		Match:      registry.Match{Ecosystems: []domain.Ecosystem{ecosystem}, Packages: []string{pkg}},
		Version:    registry.VersionStrategy{Strategy: "none"},
		Sources:    sources,
	}
	data, err := yaml.Marshal(draft)
	if err != nil {
		return fmt.Errorf("render draft manifest: %w", err)
	}
	fmt.Fprint(cmd.OutOrStdout(), string(data))
	return nil
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
