package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newDepsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "deps <project-id>",
		Short: "Show resolved project dependencies",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeps(cmd, args[0])
		},
	}
}

func runDeps(cmd *cobra.Command, projectID string) error {
	c, err := ensureDaemon(cmd.Context())
	if err != nil {
		return err
	}
	p, err := c.ProjectGet(cmd.Context(), projectID)
	if err != nil {
		return err
	}
	if !p.HasResolution {
		return fmt.Errorf("no resolution for project %s (run `depctl scan` against its root)", projectID)
	}

	out := cmd.OutOrStdout()
	if len(p.Dependencies) == 0 {
		fmt.Fprintln(out, "no dependencies")
		return nil
	}
	for _, d := range p.Dependencies {
		directness := "direct"
		if !d.Direct {
			directness = "indirect"
		}
		fmt.Fprintf(out, "%-8s %-50s %-12s %s\n", d.Ecosystem, d.Name, d.Version, directness)
	}
	return nil
}
