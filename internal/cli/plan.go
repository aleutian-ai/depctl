package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/planner"
	"aleutian-ai/ragctl/internal/query"
	"aleutian-ai/ragctl/internal/registry"
)

func newPlanCmd() *cobra.Command {
	var projectID string
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Show the desired-state sync plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPlan(cmd, projectID, jsonOut)
		},
	}
	cmd.Flags().StringVar(&projectID, "project", "", "limit to one project ID (default: every registered project)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output as JSON")
	return cmd
}

// projectPlan bundles one project's computed actions with any warning
// about that project (e.g. no resolution yet) — a warning never blocks
// planning for other projects.
type projectPlan struct {
	Project domain.Project   `json:"project"`
	Actions []planner.Action `json:"actions"`
	Warning string           `json:"warning,omitempty"`
}

// computePlans loads every registered project (or just projectID, if
// non-empty) and computes its plan. Read-only: no bbolt/Badger/network
// writes.
func computePlans(ctx context.Context, store *bboltstore.Store, backendName, projectID string) ([]projectPlan, error) {
	reg, err := loadRegistryForCLI(ctx)
	if err != nil {
		return nil, fmt.Errorf("load registry: %w", err)
	}

	var projects []domain.Project
	if projectID != "" {
		p, err := store.GetProject(ctx, projectID)
		if errors.Is(err, bboltstore.ErrNotFound) {
			// MCP-007: classified as query.ErrProjectNotFound (rather
			// than bbolt's own generic not-found) so a stale/deleted
			// project_id reaching sync_project mid-session produces
			// toolError's actionable "call scan_project first" message
			// instead of an opaque string once it crosses the daemon's
			// HTTP boundary.
			return nil, fmt.Errorf("get project %s: %w", projectID, query.ErrProjectNotFound)
		}
		if err != nil {
			return nil, fmt.Errorf("get project %s: %w", projectID, err)
		}
		projects = []domain.Project{p}
	} else {
		projects, err = store.ListProjects(ctx)
		if err != nil {
			return nil, fmt.Errorf("list projects: %w", err)
		}
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Root < projects[j].Root })

	plans := make([]projectPlan, 0, len(projects))
	for _, p := range projects {
		pp := projectPlan{Project: p}

		resolution, err := store.GetResolution(ctx, p.ID)
		if err != nil {
			pp.Warning = fmt.Sprintf("no resolution yet (run `ragctl scan` against %s)", p.Root)
			plans = append(plans, pp)
			continue
		}

		current, err := store.ListProjectReferences(ctx, p.ID)
		if err != nil {
			pp.Warning = fmt.Sprintf("read version references: %v", err)
			plans = append(plans, pp)
			continue
		}

		active := map[string]bool{}
		for _, dep := range resolution.Dependencies {
			key := planner.GenerationKey(dep)
			if _, ok := active[key]; ok {
				continue
			}
			_, err := store.GetActiveGeneration(ctx, dep.Dependency.Ecosystem, dep.Dependency.Name, backendName)
			active[key] = err == nil
		}

		actions, err := planner.Plan(ctx, p, resolution, current, reg, active)
		if err != nil {
			pp.Warning = fmt.Sprintf("plan: %v", err)
			plans = append(plans, pp)
			continue
		}
		pp.Actions = actions
		plans = append(plans, pp)
	}
	return plans, nil
}

// loadRegistryForCLI loads the registry the same way `ragctl registry
// list` does — built-in plus the data-dir user override, no
// project-level override wired in yet.
func loadRegistryForCLI(ctx context.Context) (*registry.Registry, error) {
	userDir, err := userRegistryDirPath()
	if err != nil {
		return nil, fmt.Errorf("resolve user registry dir: %w", err)
	}
	return registry.NewLoader(userDir, "").Load(ctx)
}

func runPlan(cmd *cobra.Command, projectID string, jsonOut bool) error {
	c, err := ensureDaemon(cmd.Context())
	if err != nil {
		return err
	}
	var plans []projectPlan
	if err := c.Plan(cmd.Context(), projectID, &plans); err != nil {
		return err
	}

	if jsonOut {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(plans)
	}

	printPlans(cmd, plans)
	return nil
}

func printPlans(cmd *cobra.Command, plans []projectPlan) {
	out := cmd.OutOrStdout()
	for i, pp := range plans {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "Project: %s (%s)\n", pp.Project.Root, pp.Project.ID)
		if pp.Warning != "" {
			fmt.Fprintf(out, "  warning: %s\n", pp.Warning)
			continue
		}

		nonNoop := 0
		for _, a := range pp.Actions {
			if a.Kind == planner.ActionNoop {
				continue
			}
			nonNoop++
			line := fmt.Sprintf("  %-16s %-8s %-45s %s", a.Kind, a.Dependency.Dependency.Ecosystem, a.Dependency.Dependency.Name, a.Dependency.Version)
			if a.Reason != "" {
				line += " (" + a.Reason + ")"
			}
			fmt.Fprintln(out, line)
		}
		if nonNoop == 0 {
			fmt.Fprintln(out, "  up to date")
		}
	}
}
