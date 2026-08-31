package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
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
	store, err := openControlStore()
	if err != nil {
		return fmt.Errorf("open control store: %w", err)
	}
	defer store.Close()

	ctx := context.Background()
	if _, err := store.GetProject(ctx, projectID); err != nil {
		if errors.Is(err, bboltstore.ErrNotFound) {
			return fmt.Errorf("no registered project with ID %s (run `ragctl project list` to see registered projects)", projectID)
		}
		return fmt.Errorf("get project %s: %w", projectID, err)
	}

	res, err := store.GetResolution(ctx, projectID)
	if err != nil {
		if errors.Is(err, bboltstore.ErrNotFound) {
			return fmt.Errorf("no resolution for project %s (run `ragctl scan` against its root)", projectID)
		}
		return fmt.Errorf("get resolution for %s: %w", projectID, err)
	}

	out := cmd.OutOrStdout()
	if len(res.Dependencies) == 0 {
		fmt.Fprintln(out, "no dependencies")
		return nil
	}
	for _, d := range res.Dependencies {
		directness := "direct"
		if !d.Dependency.Direct {
			directness = "indirect"
		}
		fmt.Fprintf(out, "%-8s %-50s %-12s %s\n", d.Dependency.Ecosystem, d.Dependency.Name, d.Version, directness)
	}
	return nil
}
