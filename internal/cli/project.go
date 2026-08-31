package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
)

func newProjectCmd() *cobra.Command {
	projectCmd := &cobra.Command{
		Use:   "project",
		Short: "Manage registered projects",
	}

	projectCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List registered projects",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProjectList(cmd)
		},
	})

	projectCmd.AddCommand(&cobra.Command{
		Use:   "show <project-id>",
		Short: "Show details for a registered project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProjectShow(cmd, args[0])
		},
	})

	return projectCmd
}

func runProjectList(cmd *cobra.Command) error {
	store, err := openControlStore()
	if err != nil {
		return fmt.Errorf("open control store: %w", err)
	}
	defer store.Close()

	projects, err := store.ListProjects(context.Background())
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}

	sort.Slice(projects, func(i, j int) bool { return projects[i].Root < projects[j].Root })

	out := cmd.OutOrStdout()
	if len(projects) == 0 {
		fmt.Fprintln(out, "no registered projects (run `ragctl scan` to discover some)")
		return nil
	}
	for _, p := range projects {
		fmt.Fprintf(out, "%-55s %s\n", p.ID, p.Root)
	}
	return nil
}

func runProjectShow(cmd *cobra.Command, id string) error {
	store, err := openControlStore()
	if err != nil {
		return fmt.Errorf("open control store: %w", err)
	}
	defer store.Close()

	ctx := context.Background()
	p, err := store.GetProject(ctx, id)
	if err != nil {
		if errors.Is(err, bboltstore.ErrNotFound) {
			return fmt.Errorf("no registered project with ID %s (run `ragctl project list` to see registered projects)", id)
		}
		return fmt.Errorf("get project %s: %w", id, err)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "ID:         %s\n", p.ID)
	fmt.Fprintf(out, "Root:       %s\n", p.Root)
	fmt.Fprintf(out, "Registered: %s\n", p.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(out, "Updated:    %s\n", p.UpdatedAt.Format("2006-01-02 15:04:05"))

	res, err := store.GetResolution(ctx, id)
	switch {
	case errors.Is(err, bboltstore.ErrNotFound):
		fmt.Fprintln(out, "Resolution: none (run `ragctl scan` against this project's root)")
	case err != nil:
		return fmt.Errorf("get resolution for %s: %w", id, err)
	default:
		fmt.Fprintf(out, "Resolution: %s, %d dependencies (fingerprint %s)\n", res.Ecosystem, len(res.Dependencies), res.Fingerprint)
	}
	return nil
}
