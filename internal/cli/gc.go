package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"aleutian-ai/ragctl/internal/backend"
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
	ctx := context.Background()

	store, err := openControlStore()
	if err != nil {
		return fmt.Errorf("open control store: %w", err)
	}
	defer store.Close()

	cfg, err := loadRagctlConfig()
	if err != nil {
		return err
	}

	candidates, err := retention.PlanGC(ctx, store, cfg.Vector.Backend, cfg.Retention.GracePeriod, time.Now())
	if err != nil {
		return fmt.Errorf("plan GC: %w", err)
	}

	out := cmd.OutOrStdout()
	if len(candidates) == 0 {
		fmt.Fprintln(out, "nothing eligible for garbage collection")
		return nil
	}
	for _, c := range candidates {
		fmt.Fprintf(out, "%-8s %-45s %-15s %s\n", c.Ecosystem, c.Package, c.Version, c.Reason)
	}

	if dryRun {
		return nil
	}

	badgerStore, err := openDataStore()
	if err != nil {
		return fmt.Errorf("open data store: %w", err)
	}
	defer badgerStore.Close()

	vb, err := buildVectorBackend(cfg)
	if err != nil {
		return err
	}
	ns := backend.Namespace{Name: cfg.Vector.Collection}

	results, err := gc.Run(ctx, store, badgerStore, vb, ns, candidates)
	if err != nil {
		return fmt.Errorf("run GC: %w", err)
	}

	var succeeded, failed int
	for _, r := range results {
		if r.Succeeded {
			succeeded++
			fmt.Fprintf(out, "OK    %s %s\n", r.Candidate.Package, r.Candidate.Version)
		} else {
			failed++
			fmt.Fprintf(out, "FAIL  %s %s: %s\n", r.Candidate.Package, r.Candidate.Version, r.Error)
		}
	}

	fmt.Fprintf(out, "\n%d deleted, %d failed\n", succeeded, failed)
	if failed > 0 {
		return fmt.Errorf("%d GC candidate(s) failed", failed)
	}
	return nil
}
