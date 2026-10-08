package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	bboltstore "github.com/aleutian-ai/depctl/internal/control/bbolt"
	"github.com/aleutian-ai/depctl/internal/watch"
)

// newWatchCmd keeps `depctl watch` working for anyone following an older
// README or their own muscle memory. Watching happens in the daemon now
// (ADR-011), so all this does is make sure one is running.
func newWatchCmd() *cobra.Command {
	return &cobra.Command{
		Use:        "watch",
		Short:      "Deprecated: watching is managed by the depctl daemon",
		Deprecated: "watching runs in the depctl daemon; use `depctl daemon run`, or let any command start it",
		Args:       cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWatch(cmd)
		},
	}
}

func runWatch(cmd *cobra.Command) error {
	c, err := ensureDaemon(cmd.Context())
	if err != nil {
		return err
	}
	health, err := c.Health(cmd.Context())
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "watch is now managed by the depctl daemon (pid %d, socket %s).\n", health.PID, health.Socket)
	if health.Watching {
		fmt.Fprintln(out, "it is watching registered projects; follow it with `depctl daemon status`.")
	} else {
		fmt.Fprintln(out, "watching is off (watch.enabled: false in config).")
	}
	return nil
}

// watchProjects returns every registered project that has been resolved;
// its ecosystem comes from the stored resolution. The daemon hands these
// to internal/watch.
func watchProjects(ctx context.Context, store *bboltstore.Store) ([]watch.Project, error) {
	projects, err := store.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	var out []watch.Project
	for _, p := range projects {
		res, err := store.GetResolution(ctx, p.ID)
		if errors.Is(err, bboltstore.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get resolution for %s: %w", p.ID, err)
		}
		out = append(out, watch.Project{ID: p.ID, Root: p.Root, Ecosystem: res.Ecosystem})
	}
	return out, nil
}

// resolveProject re-runs a project's resolver and stores the result. It
// runs before a watch-triggered sync, which is what turns a changed
// manifest into changed knowledge.
func resolveProject(ctx context.Context, store *bboltstore.Store, projectID string, out io.Writer) error {
	p, err := store.GetProject(ctx, projectID)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}
	prev, err := store.GetResolution(ctx, projectID)
	if err != nil {
		return fmt.Errorf("get previous resolution: %w", err)
	}
	r, ok := resolvers[prev.Ecosystem]
	if !ok {
		return fmt.Errorf("no resolver for ecosystem %q", prev.Ecosystem)
	}

	res, err := r.Resolve(ctx, p.Root)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", p.Root, err)
	}
	if err := store.PutResolution(ctx, projectID, res); err != nil {
		return fmt.Errorf("store resolution: %w", err)
	}
	fmt.Fprintf(out, "resolved %s: %d dependencies\n", p.Root, len(res.Dependencies))
	return nil
}
