package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/config"
	bboltstore "aleutian-ai/ragctl/internal/control/bbolt"
	"aleutian-ai/ragctl/internal/daemon/api"
	badgerstore "aleutian-ai/ragctl/internal/data/badger"
	"aleutian-ai/ragctl/internal/lifecycle/gc"
	"aleutian-ai/ragctl/internal/retention"
)

func newGCCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Garbage-collect unreferenced versions",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGC(cmd, dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print GC candidates without deleting anything")
	return cmd
}

func runGC(cmd *cobra.Command, dryRun bool) error {
	c, err := ensureDaemon(cmd.Context())
	if err != nil {
		return err
	}
	res, err := c.GC(cmd.Context(), dryRun, cmd.OutOrStdout())
	if err != nil {
		return err
	}
	if res.Failed > 0 {
		return fmt.Errorf("%d GC candidate(s) failed", res.Failed)
	}
	return nil
}

// RunGC plans and executes garbage collection, writing its report to
// out. It runs inside the daemon, against the stores it holds open.
func RunGC(ctx context.Context, store *bboltstore.Store, badgerStore *badgerstore.Store, cfg config.Config, dryRun bool, out io.Writer, vecReadiness *vectorReadiness) (api.GCResult, error) {
	candidates, err := retention.PlanGC(ctx, store, cfg.Vector.Backend, cfg.Retention.GracePeriod, time.Now())
	if err != nil {
		return api.GCResult{}, fmt.Errorf("plan GC: %w", err)
	}

	result := api.GCResult{Candidates: len(candidates), DryRun: dryRun}
	if len(candidates) == 0 {
		fmt.Fprintln(out, "nothing eligible for garbage collection")
		return result, nil
	}
	for _, c := range candidates {
		fmt.Fprintf(out, "%-8s %-45s %-15s %s\n", c.Ecosystem, c.Package, c.Version, c.Reason)
	}

	if dryRun {
		return result, nil
	}

	if err := vecReadiness.checkReady(); err != nil {
		return result, err
	}
	vb, err := buildVectorBackend(cfg)
	if err != nil {
		return result, err
	}
	ns := backend.Namespace{Name: cfg.Vector.Collection}

	results, err := gc.Run(ctx, store, badgerStore, vb, ns, candidates)
	if err != nil {
		return result, fmt.Errorf("run GC: %w", err)
	}

	for _, r := range results {
		if r.Succeeded {
			result.Deleted++
			fmt.Fprintf(out, "OK    %s %s\n", r.Candidate.Package, r.Candidate.Version)
		} else {
			result.Failed++
			fmt.Fprintf(out, "FAIL  %s %s: %s\n", r.Candidate.Package, r.Candidate.Version, r.Error)
		}
	}

	fmt.Fprintf(out, "\n%d deleted, %d failed\n", result.Deleted, result.Failed)
	return result, nil
}
