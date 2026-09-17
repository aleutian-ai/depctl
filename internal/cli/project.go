package cli

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"
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
	c, err := ensureDaemon(cmd.Context())
	if err != nil {
		return err
	}
	resp, err := c.ProjectList(cmd.Context())
	if err != nil {
		return err
	}

	projects := resp.Projects
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
	c, err := ensureDaemon(cmd.Context())
	if err != nil {
		return err
	}
	p, err := c.ProjectGet(cmd.Context(), id)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "ID:         %s\n", p.ID)
	fmt.Fprintf(out, "Root:       %s\n", p.Root)
	fmt.Fprintf(out, "Registered: %s\n", p.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(out, "Updated:    %s\n", p.UpdatedAt.Format("2006-01-02 15:04:05"))

	if !p.HasResolution {
		fmt.Fprintln(out, "Resolution: none (run `ragctl scan` against this project's root)")
		return nil
	}
	fmt.Fprintf(out, "Resolution: %s, %d dependencies (fingerprint %s)\n", p.Ecosystem, len(p.Dependencies), p.Fingerprint)
	return nil
}
